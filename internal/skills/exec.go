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
	"errors"
	"fmt"

	"google.golang.org/genai"
)

// ErrNoSkills is returned by NewExecutor when the skills directory exists
// and was successfully scanned but contained no SKILL.md files. Callers
// can use errors.Is to detect this case and fall back to a no-op tool.
var ErrNoSkills = errors.New("no skills found")

// Executor runs an agentskills.io-compatible agentic loop.
type Executor struct {
	model  string
	byName map[string]Skill
	names  []string
}

// NewExecutor discovers skills in dir, then creates an Executor. The caller is
// responsible for creating the client and choosing a model.
//
// Returns ErrNoSkills if dir was scanned successfully but contained no
// SKILL.md files. Other errors (e.g. dir does not exist, permission
// denied) are returned wrapped from Discover.
func NewExecutor(dir string) (*Executor, error) {

	found, err := Discover(dir)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrNoSkills
	}

	names := make([]string, len(found))
	byName := make(map[string]Skill, len(found))
	for i, s := range found {
		names[i] = s.Name
		byName[s.Name] = s
	}

	return &Executor{
		byName: byName,
		names:  names,
	}, nil
}

// HandleCall processes an 'activate_skill' or 'run_skill_script' call.
func (e *Executor) HandleCall(ctx context.Context, call *genai.FunctionCall) (*ScriptResult, error) {
	switch call.Name {
	case "activate_skill":
		name, ok := call.Args["name"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing 'name' argument")
		}
		s, ok := e.byName[name]
		if !ok {
			return nil, fmt.Errorf("unknown skill: %s", name)
		}
		body, err := s.Body()
		if err != nil {
			return nil, err
		}
		return &ScriptResult{Stdout: body}, nil

	case "run_skill_script":
		skillName, ok := call.Args["skill"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing 'skill' argument")
		}
		script, ok := call.Args["script"].(string)
		if !ok {
			return nil, fmt.Errorf("invalid or missing 'script' argument")
		}
		var args []string
		if raw, ok := call.Args["args"].([]any); ok {
			for _, a := range raw {
				if s, ok := a.(string); ok {
					args = append(args, s)
				}
			}
		}
		s, ok := e.byName[skillName]
		if !ok {
			return nil, fmt.Errorf("unknown skill: %s", skillName)
		}

		return s.RunScript(ctx, script, args)

	default:
		return nil, fmt.Errorf("unknown tool: %s", call.Name)
	}
}

// BuildTool creates the Gemini Tool definition for skill invocation.
func BuildTool(names []string) *genai.Tool {
	return &genai.Tool{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        "activate_skill",
				Description: "Load the full instructions for a named skill. Call this whenever the user task matches an available skill.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"name": {
							Type:        genai.TypeString,
							Description: "Skill name – must exactly match one of the available skills.",
							Enum:        names,
						},
					},
					Required: []string{"name"},
				},
			},
			{
				Name:        "run_skill_script",
				Description: "Execute a script from a skill's scripts/ directory. Only call this after activating the skill.",
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"skill": {
							Type:        genai.TypeString,
							Description: "The skill that owns the script.",
							Enum:        names,
						},
						"script": {
							Type:        genai.TypeString,
							Description: `Filename inside the skill's scripts/ directory, e.g. "extract.py".`,
						},
						"args": {
							Type:        genai.TypeArray,
							Description: "Optional command-line arguments to pass to the script.",
							Items:       &genai.Schema{Type: genai.TypeString},
						},
					},
					Required: []string{"skill", "script"},
				},
			},
		},
	}
}

// HasSkills returns true if the executor has any skills.
func (e *Executor) HasSkills() bool {
	return len(e.names) > 0
}

// SystemPrompt returns the agentskills.io instructional block for all skills.
func (e *Executor) SystemPrompt() string {
	if !e.HasSkills() {
		return ""
	}
	var b []Skill
	for _, name := range e.names {
		b = append(b, e.byName[name])
	}
	return SystemPrompt(b)
}

// SkillNames returns the names of all discovered skills.
func (e *Executor) SkillNames() []string {
	if !e.HasSkills() {
		return nil
	}
	return append([]string(nil), e.names...)
}
