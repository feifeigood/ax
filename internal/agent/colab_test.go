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
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/ax/proto"
)

// ---------------------------------------------------------------------------
// Pure function tests
// ---------------------------------------------------------------------------

func TestParseAccelerator(t *testing.T) {
	tests := []struct {
		input   string
		want    []string
		wantErr bool
	}{
		{input: "tpu-v5e1", want: []string{"-tpu", "v5e1"}},
		{input: "gpu-A100", want: []string{"-gpu", "A100"}},
		{input: "", want: nil},
		{input: "cpu-x86", wantErr: true},
		{input: "tpu", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseAccelerator(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q", tt.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("got[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestLastUserText(t *testing.T) {
	msg := func(role, s string) *proto.Message {
		return &proto.Message{
			Role:    role,
			Content: &proto.Content{Type: &proto.Content_Text{Text: &proto.TextContent{Text: s}}},
		}
	}

	tests := []struct {
		name     string
		messages []*proto.Message
		want     string
	}{
		{name: "empty", messages: nil, want: ""},
		{name: "single user", messages: []*proto.Message{msg("user", "hello")}, want: "hello"},
		{
			name:     "returns last user message",
			messages: []*proto.Message{msg("user", "first"), msg("assistant", "reply"), msg("user", "second")},
			want:     "second",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastUserText(tt.messages); got != tt.want {
				t.Errorf("lastUserText() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewColabAgent validation tests
// ---------------------------------------------------------------------------

func TestNewColabAgent_Validation(t *testing.T) {
	binDir := t.TempDir()
	writeFakeColab(t, binDir)
	t.Setenv("PATH", binDir)

	pyFile := writeTempFile(t, "agent.py", "# test")
	nbFile := writeTempFile(t, "notebook.ipynb", `{"cells":[]}`)

	tests := []struct {
		name     string
		cfg      ColabAgentConfig
		wantErr  string // empty = no error expected
		notebook bool
	}{
		{
			name: "py file defaults",
			cfg:  ColabAgentConfig{ID: "t", LocalFile: pyFile},
		},
		{
			name:     "ipynb detected as notebook",
			cfg:      ColabAgentConfig{ID: "t", LocalFile: nbFile},
			notebook: true,
		},
		{
			name:     "drive ipynb detected as notebook",
			cfg:      ColabAgentConfig{ID: "t", DriveFile: "MyDrive/nb.ipynb"},
			notebook: true,
		},
		{
			name:    "both local and drive",
			cfg:     ColabAgentConfig{ID: "t", LocalFile: pyFile, DriveFile: "MyDrive/nb.ipynb"},
			wantErr: "only one of",
		},
		{
			name:    "neither local nor drive",
			cfg:     ColabAgentConfig{ID: "t"},
			wantErr: "must be set",
		},
		{
			name:    "missing local file",
			cfg:     ColabAgentConfig{ID: "t", LocalFile: "/nonexistent.py"},
			wantErr: "not found",
		},
		{
			name:    "output_drive_path with drive_file",
			cfg:     ColabAgentConfig{ID: "t", DriveFile: "MyDrive/nb.ipynb", OutputDrivePath: "MyDrive/out.ipynb"},
			wantErr: "output_drive_path is only supported with local_file",
		},
		{
			name:    "malicious input_flag rejected",
			cfg:     ColabAgentConfig{ID: "t", LocalFile: pyFile, InputFlag: "x'; rm -rf /; echo '"},
			wantErr: "invalid input_flag",
		},
		{
			name:    "input_flag with dashes rejected",
			cfg:     ColabAgentConfig{ID: "t", LocalFile: pyFile, InputFlag: "--input"},
			wantErr: "invalid input_flag",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, err := NewColabAgent(tt.cfg)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("want error containing %q, got: %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if a.notebook != tt.notebook {
				t.Errorf("notebook = %v, want %v", a.notebook, tt.notebook)
			}
		})
	}
}

func TestNewColabAgent_MissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := NewColabAgent(ColabAgentConfig{LocalFile: "anything.py"})
	if err == nil || !strings.Contains(err.Error(), "colab CLI not found") {
		t.Fatalf("want colab CLI error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Connect() tests with fake colab script
// ---------------------------------------------------------------------------

func TestConnect_FullSequence(t *testing.T) {
	setup := newFakeColabEnv(t, ColabAgentConfig{
		ID:              "myagent",
		Accelerator:     "tpu-v5e1",
		DriveMountPath:  "/content/drive",
		Requirements:    "requirements.txt",
		InputFlag:       "query",
		OutputImage:     filepath.Join(t.TempDir(), "plot.png"),
		OutputDrivePath: "MyDrive/session.ipynb",
	})
	t.Setenv("COLAB_EXEC_STDOUT", "line 1\nline 2\nline 3")

	var outputs []string
	handler := OutputHandler(func(resp *proto.AgentOutputs) error {
		for _, m := range resp.Messages {
			if t := m.GetContent().GetText(); t != nil {
				outputs = append(outputs, t.Text)
			}
		}
		return nil
	})

	start := &proto.AgentStart{
		Messages: []*proto.Message{userText("hello world")},
	}

	if err := setup.agent.Connect(context.Background(), "exec-123", start, nil, handler); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Verify streaming output.
	wantOutputs := []string{"line 1", "line 2", "line 3"}
	if len(outputs) != len(wantOutputs) {
		t.Fatalf("got %d outputs %v, want %d", len(outputs), outputs, len(wantOutputs))
	}
	for i, want := range wantOutputs {
		if outputs[i] != want {
			t.Errorf("outputs[%d] = %q, want %q", i, outputs[i], want)
		}
	}

	// Verify CLI call sequence.
	cmds := setup.loggedCommands(t)
	wantCmds := []string{"new", "drivemount", "install", "upload", "exec", "download", "exec", "stop"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("got commands %v, want %v", cmds, wantCmds)
	}
	for i, want := range wantCmds {
		if cmds[i] != want {
			t.Errorf("cmd[%d] = %q, want %q", i, cmds[i], want)
		}
	}

	// Verify exec stdin contains the command with flag and user text.
	stdin := setup.readStdinLog(t)
	for _, want := range []string{"!python", "--query", "hello world"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("exec stdin missing %q: %q", want, stdin)
		}
	}
}

func TestConnect_SessionCreationFails(t *testing.T) {
	setup := newFakeColabEnv(t, ColabAgentConfig{ID: "fail-new"})
	t.Setenv("COLAB_FAIL_CMD", "new")

	start := &proto.AgentStart{Messages: []*proto.Message{userText("test")}}
	err := setup.agent.Connect(context.Background(), "e1", start, nil, noopHandler)
	if err == nil || !strings.Contains(err.Error(), "failed to create colab session") {
		t.Fatalf("want session creation error, got: %v", err)
	}

	// Stop should not be called since no session was created.
	for _, cmd := range setup.loggedCommands(t) {
		if cmd == "stop" {
			t.Error("stop should not be called when session creation fails")
		}
	}
}

func TestConnect_ExecFailure(t *testing.T) {
	setup := newFakeColabEnv(t, ColabAgentConfig{ID: "fail-exec"})
	t.Setenv("COLAB_FAIL_CMD", "exec")

	start := &proto.AgentStart{Messages: []*proto.Message{userText("test")}}
	err := setup.agent.Connect(context.Background(), "e1", start, nil, noopHandler)
	if err == nil {
		t.Fatal("expected error when exec fails")
	}

	// Stop should still run despite exec failure.
	cmds := setup.loggedCommands(t)
	if cmds[len(cmds)-1] != "stop" {
		t.Errorf("last command should be stop, got %v", cmds)
	}
}

func TestConnect_NotebookLocalFile(t *testing.T) {
	setup := newFakeColabEnv(t, ColabAgentConfig{
		ID:        "nb-local",
		InputFlag: "query",
	}, withNotebook())
	t.Setenv("COLAB_EXEC_STDOUT", "analysis complete")

	start := &proto.AgentStart{Messages: []*proto.Message{userText("analyze data")}}
	if err := setup.agent.Connect(context.Background(), "e1", start, nil, noopHandler); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Notebook: new, upload, exec (set var), exec (%run), stop.
	cmds := setup.loggedCommands(t)
	wantCmds := []string{"new", "upload", "exec", "exec", "stop"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("got commands %v, want %v", cmds, wantCmds)
	}

	// Verify stdin contains variable assignment and %run.
	stdin := setup.readStdinLog(t)
	for _, want := range []string{"query = ", "analyze data", "%run"} {
		if !strings.Contains(stdin, want) {
			t.Errorf("exec stdin missing %q: %q", want, stdin)
		}
	}
}

func TestConnect_NotebookDriveFile(t *testing.T) {
	// drive_mount_path is omitted -- the colab CLI uses its default,
	// and the code falls back to defaultDriveMountPath for filepath.Join.
	setup := newFakeColabEnv(t, ColabAgentConfig{
		ID:        "nb-drive",
		DriveFile: "MyDrive/notebooks/analysis.ipynb",
		InputFlag: "query",
	})
	t.Setenv("COLAB_EXEC_STDOUT", "done")

	start := &proto.AgentStart{Messages: []*proto.Message{userText("analyze trends")}}
	if err := setup.agent.Connect(context.Background(), "e1", start, nil, noopHandler); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Drive notebook: new, drivemount, exec (set var), exec (%run), stop. No upload.
	cmds := setup.loggedCommands(t)
	wantCmds := []string{"new", "drivemount", "exec", "exec", "stop"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("got commands %v, want %v", cmds, wantCmds)
	}

	// Verify %run uses the default mount path + drive_file.
	stdin := setup.readStdinLog(t)
	if !strings.Contains(stdin, "%run /content/drive/MyDrive/notebooks/analysis.ipynb") {
		t.Errorf("exec stdin missing %%run with drive path: %q", stdin)
	}
}

func TestConnect_RetryOnSessionTimeout(t *testing.T) {
	binDir := t.TempDir()
	writeScript(t, binDir, "colab", fakeColabTimeoutScript)

	pyFile := writeTempFile(t, "agent.py", "# test")
	logFile := filepath.Join(t.TempDir(), "colab.log")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLAB_TEST_LOG", logFile)
	t.Setenv("COLAB_EXEC_COUNTER", filepath.Join(t.TempDir(), "count"))
	t.Setenv("COLAB_EXEC_STDOUT", "success on retry")

	agent, err := NewColabAgent(ColabAgentConfig{
		ID: "timeout-test", LocalFile: pyFile, InputFlag: "input",
	})
	if err != nil {
		t.Fatalf("NewColabAgent: %v", err)
	}

	var output string
	handler := OutputHandler(func(resp *proto.AgentOutputs) error {
		for _, m := range resp.Messages {
			if txt := m.GetContent().GetText(); txt != nil {
				output = txt.Text
			}
		}
		return nil
	})

	start := &proto.AgentStart{Messages: []*proto.Message{userText("test")}}
	if err := agent.Connect(context.Background(), "e1", start, nil, handler); err != nil {
		t.Fatalf("Connect should succeed after retry: %v", err)
	}
	if output != "success on retry" {
		t.Errorf("output = %q, want %q", output, "success on retry")
	}

	// Attempt 1: new, upload, exec (fail), status (dead), stop
	// Attempt 2: new, upload, exec (ok), stop
	env := &fakeColabEnv{logFile: logFile}
	cmds := env.loggedCommands(t)
	wantCmds := []string{"new", "upload", "exec", "status", "stop", "new", "upload", "exec", "stop"}
	if len(cmds) != len(wantCmds) {
		t.Fatalf("got commands %v, want %v", cmds, wantCmds)
	}
	for i, want := range wantCmds {
		if cmds[i] != want {
			t.Errorf("cmd[%d] = %q, want %q", i, cmds[i], want)
		}
	}
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

var noopHandler = OutputHandler(func(*proto.AgentOutputs) error { return nil })

const fakeColabScript = `#!/bin/sh
echo "$*" >> "$COLAB_TEST_LOG"
if [ "$1" = "$COLAB_FAIL_CMD" ]; then
  echo "simulated failure for $1" >&2
  exit 1
fi
if [ "$1" = "exec" ]; then
  cat >> "${COLAB_TEST_LOG}.stdin"
  if [ -n "$COLAB_EXEC_STDOUT" ]; then printf '%s\n' "$COLAB_EXEC_STDOUT"; fi
  if [ -n "$COLAB_EXEC_STDERR" ]; then printf '%s' "$COLAB_EXEC_STDERR" >&2; fi
  exit 0
fi
exit 0
`

const fakeColabTimeoutScript = `#!/bin/sh
echo "$*" >> "$COLAB_TEST_LOG"
if [ "$1" = "status" ]; then exit 1; fi
if [ "$1" = "exec" ]; then
  cat >> "${COLAB_TEST_LOG}.stdin"
  count=0
  if [ -f "$COLAB_EXEC_COUNTER" ]; then count=$(cat "$COLAB_EXEC_COUNTER"); fi
  count=$((count + 1))
  echo "$count" > "$COLAB_EXEC_COUNTER"
  if [ "$count" -eq 1 ]; then echo "session not found" >&2; exit 1; fi
  if [ -n "$COLAB_EXEC_STDOUT" ]; then printf '%s\n' "$COLAB_EXEC_STDOUT"; fi
  exit 0
fi
exit 0
`

type envOpt func(cfg *ColabAgentConfig)

func withNotebook() envOpt {
	return func(cfg *ColabAgentConfig) {
		cfg.LocalFile = "" // will be set to notebook below
	}
}

type fakeColabEnv struct {
	agent   *ColabAgent
	logFile string
}

func newFakeColabEnv(t *testing.T, cfg ColabAgentConfig, opts ...envOpt) *fakeColabEnv {
	t.Helper()

	binDir := t.TempDir()
	writeFakeColab(t, binDir)
	logFile := filepath.Join(t.TempDir(), "colab.log")

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COLAB_TEST_LOG", logFile)

	isNb := false
	for _, opt := range opts {
		opt(&cfg)
		isNb = true
	}

	// Create a temp file if LocalFile is needed and not already set.
	if cfg.LocalFile == "" && cfg.DriveFile == "" {
		if isNb {
			cfg.LocalFile = writeTempFile(t, "test.ipynb", `{"cells":[]}`)
		} else {
			cfg.LocalFile = writeTempFile(t, "test.py", "# test")
		}
	}

	if cfg.ID == "" {
		cfg.ID = "test-agent"
	}

	agent, err := NewColabAgent(cfg)
	if err != nil {
		t.Fatalf("NewColabAgent: %v", err)
	}
	return &fakeColabEnv{agent: agent, logFile: logFile}
}

func (e *fakeColabEnv) readLog(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(e.logFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("readLog: %v", err)
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func (e *fakeColabEnv) loggedCommands(t *testing.T) []string {
	t.Helper()
	lines := e.readLog(t)
	cmds := make([]string, len(lines))
	for i, line := range lines {
		cmds[i] = strings.Fields(line)[0]
	}
	return cmds
}

func (e *fakeColabEnv) readStdinLog(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(e.logFile + ".stdin")
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("readStdinLog: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func writeFakeColab(t *testing.T, dir string) {
	t.Helper()
	writeScript(t, dir, "colab", fakeColabScript)
}

func writeScript(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0755); err != nil {
		t.Fatalf("write script %s: %v", name, err)
	}
}

func writeTempFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func userText(s string) *proto.Message {
	return &proto.Message{
		Role:    "user",
		Content: &proto.Content{Type: &proto.Content_Text{Text: &proto.TextContent{Text: s}}},
	}
}
