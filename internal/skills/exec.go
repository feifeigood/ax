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
	"io"
	"os"
	"path/filepath"

	"google.golang.org/genai"
)

// UpdateKind identifies the type of an UpdateEvent.
type UpdateKind int

const (
	UpdateSkillActivated UpdateKind = iota
	UpdateScriptRunning
	UpdateScriptDone
)

// UpdateEvent describes a state change during execution.
type UpdateEvent struct {
	Kind     UpdateKind
	Skill    string
	Script   string // UpdateScriptRunning and UpdateScriptDone
	ExitCode int    // UpdateScriptDone only
}

// Executor runs an agentskills.io-compatible agentic loop.
type Executor struct {
	client *genai.Client
	model  string
	cfg    *genai.GenerateContentConfig

	byName   map[string]Skill
	onUpdate func(UpdateEvent)
}

// OnUpdate registers a callback that is called when a skill is activated or a
// script is run. Replaces any previously registered callback.
func (e *Executor) OnUpdate(fn func(UpdateEvent)) {
	e.onUpdate = fn
}

func (e *Executor) update(ev UpdateEvent) {
	if e.onUpdate != nil {
		e.onUpdate(ev)
	}
}

// NewExecutor discovers skills in dir, then creates an Executor. The caller is
// responsible for creating the client and choosing a model.
func NewExecutor(client *genai.Client, model, dir string) (*Executor, error) {
	if dir == "" {
		dir = os.Getenv("SKILLS_DIR")
	}
	if dir == "" {
		dir = filepath.Join(os.Getenv("HOME"), ".agents", "skills")
	}

	found, err := Discover(dir)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, io.EOF
	}

	names := make([]string, len(found))
	byName := make(map[string]Skill, len(found))
	for i, s := range found {
		names[i] = s.Name
		byName[s.Name] = s
	}

	si := "You are a helpful AI assistant with access to a set of skills.\n" +
		"When a user task matches one or more skills, call activate_skill to load\n" +
		"its full instructions before answering.\n\n" +
		systemPrompt(found)

	cfg := &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(si, genai.RoleUser),
		Tools:             []*genai.Tool{buildTool(names)},
	}

	return &Executor{
		client: client,
		byName: byName,
		cfg:    cfg,
		model:  model,
	}, nil
}

// Run sends task to the model and returns the final text response, activating
// skills and executing scripts as requested by the model along the way.
func (e *Executor) Run(ctx context.Context, prompt string) (string, error) {
	history := []*genai.Content{
		genai.NewContentFromText(prompt, genai.RoleUser),
	}

	for {
		resp, err := e.client.Models.GenerateContent(ctx, e.model, history, e.cfg)
		if err != nil {
			return "", err
		}
		if len(resp.Candidates) > 0 && resp.Candidates[0].Content != nil {
			history = append(history, resp.Candidates[0].Content)
		}

		calls := resp.FunctionCalls()
		if len(calls) == 0 {
			return resp.Text(), nil
		}

		var results []*genai.Part
		for _, call := range calls {
			results = append(results, e.handleCall(ctx, call))
		}
		history = append(history, genai.NewContentFromParts(results, genai.RoleUser))
	}
}

func (e *Executor) handleCall(ctx context.Context, call *genai.FunctionCall) *genai.Part {
	respond := func(v map[string]any) *genai.Part {
		return genai.NewPartFromFunctionResponse(call.Name, v)
	}

	switch call.Name {
	case "activate_skill":
		name, ok := call.Args["name"].(string)
		if !ok {
			return respond(map[string]any{"error": "invalid or missing 'name' argument"})
		}
		s, ok := e.byName[name]
		if !ok {
			return respond(map[string]any{"error": "unknown skill: " + name})
		}
		e.update(UpdateEvent{Kind: UpdateSkillActivated, Skill: name})
		body, err := s.Body()
		if err != nil {
			return respond(map[string]any{"error": err.Error()})
		}
		return respond(map[string]any{"instructions": body})

	case "run_skill_script":
		skillName, ok := call.Args["skill"].(string)
		if !ok {
			return respond(map[string]any{"error": "invalid or missing 'skill' argument"})
		}
		script, ok := call.Args["script"].(string)
		if !ok {
			return respond(map[string]any{"error": "invalid or missing 'script' argument"})
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
			return respond(map[string]any{"error": "unknown skill: " + skillName})
		}
		e.update(UpdateEvent{Kind: UpdateScriptRunning, Skill: skillName, Script: script})
		result, err := s.RunScript(ctx, script, args)
		if err != nil {
			return respond(map[string]any{"error": err.Error()})
		}
		e.update(UpdateEvent{Kind: UpdateScriptDone, Skill: skillName, Script: script, ExitCode: result.ExitCode})
		return respond(map[string]any{
			"stdout":    result.Stdout,
			"stderr":    result.Stderr,
			"exit_code": result.ExitCode,
		})

	default:
		return respond(map[string]any{"error": "unknown tool: " + call.Name})
	}
}

func buildTool(names []string) *genai.Tool {
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
