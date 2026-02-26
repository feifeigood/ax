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

// Package skills implements the agentskills.io discovery and execution protocol.
//
// Skills are directories containing a SKILL.md file with YAML frontmatter
// (name, description) followed by Markdown instructions. An optional scripts/
// subdirectory may contain executable files that agents can run.
package skills

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxSkillFileSize = 32 * 1024 // 32 KB

// Skill represents a discovered agentskills.io skill.
type Skill struct {
	Name        string
	Description string
	Dir         string // absolute path to the skill directory
}

// ScriptResult holds the output of a RunScript call.
type ScriptResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// DefaultDir returns the default system-wide directory for discovering skills.
// It resolves to ~/.agents/skills on all platforms.
func DefaultDir() string {
    homeDir, err := os.UserHomeDir()
    if err != nil {
        // Fallback to Unix HOME if UserHomeDir fails (though rare).
        homeDir = os.Getenv("HOME")
    }
    return filepath.Join(homeDir, ".agents", "skills")
}

// Discover scans dir for agentskills.io-compatible skill directories and
// returns their parsed metadata. Invisible entries (names starting with ".")
// and entries that are not directories (including broken symlinks) are skipped.
// Directories that lack a valid SKILL.md are silently skipped.
func Discover(dir string) ([]Skill, error) {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve skills dir path %s: %w", dir, err)
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("read skills dir %s: %w", absDir, err)
	}

	var out []Skill
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue
		}
		skillDir := filepath.Join(absDir, e.Name())
		// os.Stat follows symlinks, so symlinked skill directories are supported.
		info, err := os.Stat(skillDir)
		if err != nil || !info.IsDir() {
			continue
		}
		raw, err := readAndTruncate(filepath.Join(skillDir, "SKILL.md"), maxSkillFileSize)
		if err != nil {
			continue
		}
		s, err := parseMetadata(raw)
		if err != nil {
			continue
		}
		s.Dir = skillDir
		out = append(out, s)
	}
	return out, nil
}

// Body returns the Markdown body of the skill's SKILL.md with the YAML
// frontmatter stripped. This is the full instruction text to inject into a
// model context when the skill is activated.
func (s Skill) Body() (string, error) {
	raw, err := os.ReadFile(filepath.Join(s.Dir, "SKILL.md"))
	if err != nil {
		return "", err
	}
	content := string(raw)
	if strings.HasPrefix(content, "---") {
		rest := content[3:]
		_, after, ok := strings.Cut(rest, "\n---")
		if !ok {
			return "", fmt.Errorf("unclosed frontmatter in %s", filepath.Join(s.Dir, "SKILL.md"))
		}
		content = after
	}
	return strings.TrimSpace(content), nil
}

// RunScript executes scripts/<script> inside the skill directory and returns
// its stdout, stderr, and exit code. script must be a plain filename with no
// path separators to prevent traversal outside the scripts/ directory.
func (s Skill) RunScript(ctx context.Context, script string, args []string) (*ScriptResult, error) {
	if filepath.Base(script) != script {
		return nil, fmt.Errorf("invalid script name: %q", script)
	}
	scriptPath := filepath.Join(s.Dir, "scripts", script)

	if _, err := os.Stat(scriptPath); err != nil {
		return nil, fmt.Errorf("script %q not found or inaccessible: %w", script, err)
	}

	cmd := exec.CommandContext(ctx, scriptPath, args...)
	cmd.Dir = s.Dir
	
	var stdout, stderr bytes.Buffer	
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	exitCode := 0
	if err := cmd.Run(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exitCode = ee.ExitCode()
		} else {
			return nil, err
		}
	}
	return &ScriptResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}, nil
}

// SystemPrompt returns the <available_skills> XML block for injection into a
// model system prompt, following the agentskills.io integration guide.
func SystemPrompt(skills []Skill) string {
	var b strings.Builder
	b.WriteString("<available_skills>\n")
	for _, s := range skills {
		b.WriteString("  <skill>\n")
		fmt.Fprintf(&b, "    <name>%s</name>\n", s.Name)
		fmt.Fprintf(&b, "    <description>%s</description>\n", s.Description)
		b.WriteString("  </skill>\n")
	}
	b.WriteString("</available_skills>")
	return b.String()
}

// parseMetadata extracts skill metadata from raw SKILL.md content.
func parseMetadata(content []byte) (Skill, error) {
	s := string(content)
	if !strings.HasPrefix(s, "---") {
		return Skill{}, fmt.Errorf("no frontmatter")
	}
	yamlBlock, _, ok := strings.Cut(s[3:], "\n---")
	if !ok {
		return Skill{}, fmt.Errorf("unclosed frontmatter")
	}
	var fm struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
		return Skill{}, fmt.Errorf("invalid YAML: %w", err)
	}
	if fm.Name == "" || fm.Description == "" {
		return Skill{}, fmt.Errorf("name and description are required")
	}
	return Skill{Name: fm.Name, Description: fm.Description}, nil
}

func readAndTruncate(filename string, limit int64) ([]byte, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Will read up to 'limit' bytes and stop, returning no error for truncation
	lr := io.LimitReader(f, limit)
	return io.ReadAll(lr)
}
