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

package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDiscover(t *testing.T) {
	// Create a temporary skills directory
	tmpDir := t.TempDir()

	// 1. Valid skill
	skill1Dir := filepath.Join(tmpDir, "test-skill-1")
	os.MkdirAll(skill1Dir, 0755)
	err := os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte("---\nname: Test Skill 1\ndescription: Description 1\n---\nBody 1"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Skill without SKILL.md (should be skipped)
	skill2Dir := filepath.Join(tmpDir, "test-skill-2")
	os.MkdirAll(skill2Dir, 0755)

	// 3. Skill with invalid frontmatter (should be skipped)
	skill3Dir := filepath.Join(tmpDir, "test-skill-3")
	os.MkdirAll(skill3Dir, 0755)
	os.WriteFile(filepath.Join(skill3Dir, "SKILL.md"), []byte("invalid content"), 0644)

	skills, err := Discover(tmpDir)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}

	if len(skills) != 1 {
		t.Errorf("Expected 1 skill, got %d", len(skills))
	}

	if skills[0].Name != "Test Skill 1" {
		t.Errorf("Expected name 'Test Skill 1', got %q", skills[0].Name)
	}
}

func TestSkillBody(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "test-skill")
	os.MkdirAll(skillDir, 0755)
	content := "---\nname: Test\ndescription: Test\n---\nThis is the body content."
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(content), 0644)

	s := Skill{Dir: skillDir}
	body, err := s.Body()
	if err != nil {
		t.Fatalf("Body() failed: %v", err)
	}

	expected := "This is the body content."
	if body != expected {
		t.Errorf("Expected body %q, got %q", expected, body)
	}
}

func TestRunScript(t *testing.T) {
	tmpDir := t.TempDir()
	skillDir := filepath.Join(tmpDir, "test-skill")
	scriptsDir := filepath.Join(skillDir, "scripts")
	os.MkdirAll(scriptsDir, 0755)

	scriptContent := "#!/bin/sh\necho \"hello $1\""
	scriptPath := filepath.Join(scriptsDir, "hello.sh")
	os.WriteFile(scriptPath, []byte(scriptContent), 0755)

	ctx := context.Background()
	s := Skill{Dir: skillDir, Name: "test-skill"}

	result, err := s.RunScript(ctx, "hello.sh", []string{"world"})
	if err != nil {
		t.Fatalf("RunScript() failed: %v", err)
	}

	if result.ExitCode != 0 {
		t.Errorf("Expected exit code 0, got %d", result.ExitCode)
	}

	expectedStdout := "hello world\n"
	if result.Stdout != expectedStdout {
		t.Errorf("Expected stdout %q, got %q", expectedStdout, result.Stdout)
	}
}
