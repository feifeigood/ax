// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/proto"
)

// ColabAgent implements the Agent interface by executing a Python file or
// Jupyter notebook on a remote Google Colab session via the colab CLI.
//
// Each Connect() call provisions a new ephemeral Colab session, sets up the
// environment (drive mount, package installation), uploads the file (if local),
// executes it with user input, and tears the session down on completion.
//
// Two execution modes are supported:
//   - Python scripts (.py): executed via !python with input passed as a CLI flag.
//   - Notebooks (.ipynb): executed via %run with input set as a Python variable.
type ColabAgent struct {
	config          ColabAgentConfig
	acceleratorArgs []string // parsed from config.Accelerator, e.g. ["-tpu", "v5e1"]
	notebook        bool     // true if the file is a .ipynb notebook
	mu              sync.Mutex
	activeSessions  map[string]struct{} // all currently running sessions, for Close() cleanup
}

// ColabAgentConfig configures a Colab agent.
type ColabAgentConfig struct {
	ID              string
	LocalFile       string // Path to a local .py or .ipynb file (uploaded to VM)
	DriveFile       string // Path to .ipynb file in Google Drive (e.g. MyDrive/notebooks/nb.ipynb)
	Accelerator     string // Accelerator type (optional), e.g. "tpu-v5e1", "gpu-A100"
	DriveMountPath  string // Path to mount Google Drive (optional), default: "/content/drive"
	Requirements    string // Path to requirements.txt (optional)
	InputFlag       string // Name of the input parameter (optional). For .py files, passed as --<name>. For .ipynb, set as a variable before %run
	OutputImage     string // Local path to download the output image to
	OutputDrivePath string // Google Drive path to save converted .ipynb (e.g. MyDrive/notebooks/out.ipynb)
}

// NewColabAgent creates a new ColabAgent. It validates that the colab CLI
// binary is available in PATH and that exactly one of LocalFile or RemoteFile
// is set.
func NewColabAgent(config ColabAgentConfig) (*ColabAgent, error) {
	// Validate that the colab CLI is installed.
	if _, err := exec.LookPath("colab"); err != nil {
		return nil, fmt.Errorf("colab CLI not found in PATH: %w", err)
	}

	// Exactly one of LocalFile or DriveFile must be set.
	if config.LocalFile == "" && config.DriveFile == "" {
		return nil, fmt.Errorf("one of local_file or drive_file must be set")
	}
	if config.LocalFile != "" && config.DriveFile != "" {
		return nil, fmt.Errorf("only one of local_file or drive_file can be set, not both")
	}

	// OutputDrivePath is only supported for local files.
	if config.OutputDrivePath != "" && config.LocalFile == "" {
		return nil, fmt.Errorf("output_drive_path is only supported with local_file")
	}

	// Validate the local file exists (Drive files are on the VM, can't check locally).
	if config.LocalFile != "" {
		if _, err := os.Stat(config.LocalFile); err != nil {
			return nil, fmt.Errorf("local file %q not found: %w", config.LocalFile, err)
		}
	}

	// Parse the accelerator string into CLI flags.
	accelArgs, err := parseAccelerator(config.Accelerator)
	if err != nil {
		return nil, err
	}

	// Validate InputFlag is a safe identifier to prevent code injection.
	// It's used directly in Python variable assignments and shell command flags.
	if config.InputFlag != "" && !validIdentifier.MatchString(config.InputFlag) {
		return nil, fmt.Errorf("invalid input_flag %q: must be a valid identifier (letters, digits, underscores)", config.InputFlag)
	}

	// Detect notebook mode from file extension.
	notebook := isNotebook(config.LocalFile) || isNotebook(config.DriveFile)

	return &ColabAgent{
		config:          config,
		acceleratorArgs: accelArgs,
		notebook:        notebook,
		activeSessions:  make(map[string]struct{}),
	}, nil
}

// validIdentifier matches a valid Python identifier (also valid as a CLI flag name).
// Used to validate InputFlag to prevent code injection via malformed ax.yaml.
var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// isNotebook returns true if the file path has a .ipynb extension.
func isNotebook(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".ipynb")
}

// parseAccelerator converts an accelerator string like "tpu-v5e1" or "gpu-A100"
// into colab CLI flags like ["-tpu", "v5e1"] or ["-gpu", "A100"].
// An empty string returns nil (no accelerator flags).
func parseAccelerator(accel string) ([]string, error) {
	if accel == "" {
		return nil, nil
	}
	parts := strings.SplitN(accel, "-", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid accelerator format %q: expected \"tpu-<type>\" or \"gpu-<type>\"", accel)
	}
	kind := strings.ToLower(parts[0])
	if kind != "tpu" && kind != "gpu" {
		return nil, fmt.Errorf("invalid accelerator kind %q: must be \"tpu\" or \"gpu\"", kind)
	}
	return []string{"-" + kind, parts[1]}, nil
}

const (
	// maxRetries is the number of times Connect will retry if the Colab session
	// is terminated due to idle timeout.
	maxRetries = 1

	// defaultDriveMountPath is the standard mount path used by the Colab CLI
	// when no path is specified. Used as a fallback for filepath.Join when
	// drive_mount_path is omitted in the config.
	defaultDriveMountPath = "/content/drive"
)

// Connect provisions a new Colab session, sets up the environment, executes
// the agent code, and tears the session down. If the session is terminated
// due to idle timeout (e.g. while the user is authorizing Drive access),
// it automatically recreates the session and retries once.
func (a *ColabAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	sessionName := colabSessionName(a.config.ID, execID)

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Create a new Colab session (or recreate after timeout).
		newArgs := []string{"new", "-s", sessionName}
		newArgs = append(newArgs, a.acceleratorArgs...)
		if _, err := a.runColab(ctx, newArgs...); err != nil {
			return fmt.Errorf("failed to create colab session: %w", err)
		}

		// Track the active session for Close() cleanup.
		a.mu.Lock()
		a.activeSessions[sessionName] = struct{}{}
		a.mu.Unlock()

		// Run the setup and execution steps.
		err := a.run(ctx, sessionName, start, o)

		if err == nil {
			a.stopSession(sessionName)
			return nil
		}

		// Check if the session died on its own (idle timeout) BEFORE
		// stopping it, so we can distinguish timeout from other errors.
		sessionDied := !a.isSessionAlive(ctx, sessionName)
		a.stopSession(sessionName)

		// If the session timed out and we have retries left, recreate
		// the session and try again.
		if attempt < maxRetries && sessionDied {
			log.Printf("Colab session %s timed out, retrying...", sessionName)
			continue
		}

		return err
	}

	return fmt.Errorf("colab session timed out after %d retries", maxRetries)
}

// run executes the setup and agent code on an existing Colab session.
// This is called by Connect and may be retried if the session times out.
func (a *ColabAgent) run(ctx context.Context, sessionName string, start *proto.AgentStart, o agent.OutputHandler) error {
	// Mount Google Drive if any Drive feature is used (drive_file,
	// output_drive_path, or explicit drive_mount_path). This step is
	// interactive because drivemount may prompt for OAuth authorization
	// (the user must open a URL in their browser, authorize, then press Enter).
	needsDrive := a.config.DriveMountPath != "" || a.config.DriveFile != "" || a.config.OutputDrivePath != ""
	if needsDrive {
		mountCtx, mountCancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer mountCancel()
		log.Println("Mounting Google Drive (follow the authorization prompt, then press Enter)...")

		// If drive_mount_path is set, pass it to the CLI. Otherwise let
		// the Colab CLI use its default mount path.
		args := []string{"drivemount", "-s", sessionName}
		if a.config.DriveMountPath != "" {
			args = append(args, a.config.DriveMountPath)
		}
		if err := a.runColabInteractive(mountCtx, args...); err != nil {
			return fmt.Errorf("failed to mount drive: %w", err)
		}

		// Use the Colab CLI's default mount path for filepath.Join
		// when constructing VM paths for DriveFile and OutputDrivePath.
		if a.config.DriveMountPath == "" {
			a.config.DriveMountPath = defaultDriveMountPath
		}
	}

	// Install requirements if configured.
	if a.config.Requirements != "" {
		if _, err := a.runColab(ctx, "install", "-s", sessionName, "-r", a.config.Requirements); err != nil {
			return fmt.Errorf("failed to install requirements: %w", err)
		}
	}

	// Determine the remote path for execution. If LocalFile is set, upload
	// it to the VM first. If RemoteFile is set, use it directly (e.g. a
	// notebook on Google Drive that is accessible after drivemount).
	var remotePath string
	if a.config.LocalFile != "" {
		remotePath = "/content/" + filepath.Base(a.config.LocalFile)
		if _, err := a.runColab(ctx, "upload", "-s", sessionName, a.config.LocalFile, remotePath); err != nil {
			return fmt.Errorf("failed to upload %s: %w", a.config.LocalFile, err)
		}
	} else {
		remotePath = filepath.Join(a.config.DriveMountPath, a.config.DriveFile)
	}

	// Extract the latest user text from the message history.
	userText := lastUserText(start.Messages)
	pyInput, _ := json.Marshal(userText)
	shellEscaped := strings.ReplaceAll(userText, "'", "'\\''")

	if a.notebook {
		// Notebook execution.
		// If input_flag is set, set the input variable in the kernel first
		// (output suppressed), then run the notebook.
		if a.config.InputFlag != "" {
			setVarCmd := fmt.Sprintf("%s = %s", a.config.InputFlag, pyInput)
			if _, err := a.runColabExecBatch(ctx, sessionName, setVarCmd); err != nil {
				return fmt.Errorf("failed to set input variable: %w", err)
			}
		}

		// Run the notebook (output streamed).
		runCmd := fmt.Sprintf("%%run %s", remotePath)
		if err := a.runColabExec(ctx, sessionName, runCmd, o); err != nil {
			return err
		}
	} else {
		// Python script execution (output streamed).
		// -u disables stdout buffering so output streams line-by-line
		// (Python block-buffers when stdout is not a TTY).
		command := fmt.Sprintf("!python -u %s", remotePath)

		// If input_flag is set, pass user input as a CLI flag.
		if a.config.InputFlag != "" {
			command += fmt.Sprintf(" --%s '%s'", a.config.InputFlag, shellEscaped)
		}

		// Pass the output image path to the script if configured.
		if a.config.OutputImage != "" {
			remoteImagePath := "/content/" + filepath.Base(a.config.OutputImage)
			command += fmt.Sprintf(" --output '%s'", remoteImagePath)
		}

		// Execute and stream output line-by-line.
		if err := a.runColabExec(ctx, sessionName, command, o); err != nil {
			return err
		}
	}

	// Download the output image from the Colab VM if configured.
	if a.config.OutputImage != "" {
		imageRemotePath := "/content/" + filepath.Base(a.config.OutputImage)
		if _, err := a.runColab(ctx, "download", "-s", sessionName, imageRemotePath, a.config.OutputImage); err != nil {
			return fmt.Errorf("failed to download output image: %w", err)
		}
	}

	// Convert the .py file to a .ipynb notebook and save it to Google Drive
	// (local_file only). The output path is constructed from DriveMountPath +
	// OutputDrivePath (e.g. /content/drive + MyDrive/nb.ipynb). Uses nbformat
	// (pre-installed in Colab) to create a notebook with the script's source
	// code as a single code cell.
	if a.config.OutputDrivePath != "" && a.config.LocalFile != "" {
		outputNotebookPath := filepath.Join(a.config.DriveMountPath, a.config.OutputDrivePath)
		remotePathLit, _ := json.Marshal(remotePath)
		outputNbLit, _ := json.Marshal(outputNotebookPath)
		convertCmd := fmt.Sprintf(
			"import nbformat; nb = nbformat.v4.new_notebook(); nb.cells.append(nbformat.v4.new_code_cell(open(%s).read())); nbformat.write(nb, %s)",
			remotePathLit, outputNbLit,
		)
		if _, err := a.runColabExecBatch(ctx, sessionName, convertCmd); err != nil {
			return fmt.Errorf("failed to convert script to notebook: %w", err)
		}
	}

	return nil
}

// stopSession stops a Colab session and removes it from the active sessions tracker.
func (a *ColabAgent) stopSession(sessionName string) {
	stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if _, err := a.runColab(stopCtx, "stop", "-s", sessionName); err != nil {
		log.Printf("Warning: failed to stop colab session %s: %v", sessionName, err)
	}
	a.mu.Lock()
	delete(a.activeSessions, sessionName)
	a.mu.Unlock()
}

// isSessionAlive checks whether a Colab session still exists by running
// colab status. Returns false if the session was terminated (e.g. due to
// idle timeout).
func (a *ColabAgent) isSessionAlive(ctx context.Context, sessionName string) bool {
	_, err := a.runColab(ctx, "status", "-s", sessionName)
	return err == nil
}

// Close stops all currently active Colab sessions. This handles the case where
// Close() is called (e.g. via SIGINT) while one or more Connect() calls are
// mid-execution. In ax serve mode, multiple concurrent Connect() calls may
// each have their own session.
func (a *ColabAgent) Close() error {
	a.mu.Lock()
	sessions := make([]string, 0, len(a.activeSessions))
	for s := range a.activeSessions {
		sessions = append(sessions, s)
	}
	a.mu.Unlock()

	if len(sessions) == 0 {
		return nil
	}

	var firstErr error
	for _, session := range sessions {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		if _, err := a.runColab(ctx, "stop", "-s", session); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("failed to stop colab session %s: %w", session, err)
		}
		cancel()
	}
	return firstErr
}

// runColab executes a colab CLI command and returns its stdout.
// Only the exit code determines success or failure; stderr is included
// in the error message on failure but is not treated as an error by itself
// (setup commands like install write progress to stderr).
func (a *ColabAgent) runColab(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "colab", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("colab %s: %w\nstderr: %s", args[0], err, stderr.String())
	}
	return stdout.String(), nil
}

// runColabInteractive executes a colab CLI command with the user's terminal
// connected for interactive I/O. This is required for commands like drivemount
// that may prompt for OAuth authorization (the user must open a URL, authorize,
// then press Enter).
//
// Both stdout and stderr from the colab process are routed to os.Stderr rather
// than os.Stdout. This is necessary because the ax CLI's display system
// (Display.DisplayOutput in cmd/ax/internal/display.go) writes to stdout and
// would interfere with the colab process's multi-line auth message -- only a
// small portion would be visible. Routing to stderr bypasses the display layer
// and ensures the full auth prompt is shown to the user.
func (a *ColabAgent) runColabInteractive(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "colab", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr // Use stderr to bypass ax CLI display layer (see comment above).
	cmd.Stderr = os.Stderr
	// Set a request timeout for the colab CLI. The default is 60s, which is too
	// short for interactive commands like drivemount where the user needs time
	// to authorize in the browser.
	cmd.Env = append(os.Environ(), "REQUEST_TIMEOUT=600")
	return cmd.Run()
}

// runColabExecBatch pipes Python code to colab exec via stdin and waits for
// completion. Output is discarded. Used for setup commands like setting
// variables in the kernel before running a notebook.
func (a *ColabAgent) runColabExecBatch(ctx context.Context, sessionName, command string) (string, error) {
	cmd := exec.CommandContext(ctx, "colab", "exec", "-s", sessionName)
	cmd.Stdin = strings.NewReader(command)
	var stdoutBuf, stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("colab exec: %w\nstderr: %s", err, stderrBuf.String())
	}
	return stdoutBuf.String(), nil
}

// runColabExec pipes Python code to colab exec via stdin and streams stdout
// line-by-line to the OutputHandler as the script runs. Stderr is buffered
// and treated as an error after the process exits.
func (a *ColabAgent) runColabExec(ctx context.Context, sessionName, command string, o agent.OutputHandler) error {
	cmd := exec.CommandContext(ctx, "colab", "exec", "-s", sessionName)
	cmd.Stdin = strings.NewReader(command)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	var stderrBuf bytes.Buffer
	cmd.Stderr = &stderrBuf

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start colab exec: %w", err)
	}

	// Stream stdout line-by-line to the output handler.
	// Skip empty lines to avoid extra blank lines from colab exec
	// (IPython's ! command often emits a trailing empty line).
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		if err := o(&proto.AgentOutputs{
			Messages: []*proto.Message{{
				Role: "assistant",
				Content: &proto.Content{
					Type: &proto.Content_Text{
						Text: &proto.TextContent{Text: line},
					},
				},
			}},
		}); err != nil {
			return fmt.Errorf("output handler: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading stdout: %w", err)
	}

	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("%w\nstderr: %s", err, stderrBuf.String())
	}
	return nil
}

// colabSessionName builds a deterministic session name from the agent ID
// and execution ID. Colab session names should be short and safe.
func colabSessionName(agentID, execID string) string {
	safeID := strings.ReplaceAll(execID, "-", "")
	if len(safeID) > 20 {
		safeID = safeID[:20]
	}
	return fmt.Sprintf("ax-%s-%s", agentID, safeID)
}

// lastUserText extracts the text from the most recent user message
// in the message history.
func lastUserText(messages []*proto.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		msg := messages[i]
		if msg.Role == "user" {
			if t := msg.GetContent().GetText(); t != nil {
				return t.Text
			}
		}
	}
	return ""
}
