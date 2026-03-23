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

package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/golang/protobuf/ptypes/duration"
	"github.com/google/ax/agent"
	"github.com/google/ax/internal/skills"
	"github.com/google/ax/proto"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/structpb"
)

// GeminiAgent implements task.Agent using Gemini.
type GeminiAgent struct {
}

// NewGeminiAgent creates a new Gemini agent.
func NewGeminiAgent() *GeminiAgent {
	return &GeminiAgent{}
}

func (a *GeminiAgent) config(start *proto.AgentStart) (*proto.GeminiConfig, error) {
	if start.Config == nil {
		return &proto.GeminiConfig{
			Model:   "gemini-3-flash-preview",
			Timeout: &duration.Duration{Seconds: 30},
		}, nil
	}

	var cfg proto.GeminiConfig
	if err := start.Config.UnmarshalTo(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal Gemini config: %w", err)
	}
	return &cfg, nil
}

func (a *GeminiAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	cfg, err := a.config(start)
	if err != nil {
		return err
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: os.Getenv("GEMINI_API_KEY"),
		// TODO(jbd): Support Vertex credentials.
	})
	if err != nil {
		return fmt.Errorf("failed to create Gemini client: %w", err)
	}

	inputs := start.Contents
	contents := protoToContents(inputs)
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout.AsDuration())
	defer cancel()

	var systemPrompt *genai.Content
	if cfg.SystemPrompt != "" {
		systemPrompt = genai.Text(cfg.SystemPrompt)[0]
	}
	resp, err := client.Models.GenerateContent(ctx, cfg.Model, contents, &genai.GenerateContentConfig{
		SystemInstruction: systemPrompt,
		MaxOutputTokens:   cfg.MaxTokens,
		CandidateCount:    1,
	})
	if err != nil {
		return fmt.Errorf("failed to generate: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return fmt.Errorf("no candidates from Gemini in planner")
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil || candidate.Content.Parts == nil {
		return fmt.Errorf("no content in candidates from Gemini")
	}

	respCount := 0
	for _, part := range candidate.Content.Parts {
		if part == nil {
			continue
		}
		if part.Text != "" {
			respCount++
			if err := handler(&proto.AgentOutputs{
				Contents: []*proto.Content{{
					Role: "model",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{Text: part.Text},
					},
				}},
			}); err != nil {
				return err
			}
		}
	}
	if respCount == 0 {
		return errors.New("no responses from Gemini")
	}
	return nil
}

func (a *GeminiAgent) HealthCheck(ctx context.Context) error {
	return nil
}

func (a *GeminiAgent) Close() error {
	return nil
}

type Tool interface {
	Name() string
	FuncDecl() []*genai.Tool
	HandleCall(ctx context.Context, fc *genai.FunctionCall, o agent.OutputHandler) error
	HandleExecute(ctx context.Context, fc *genai.FunctionCall, approved bool, o agent.OutputHandler) error
}

type BashTool struct{}

func (t *BashTool) Name() string {
	return "bash"
}

func (t *BashTool) FuncDecl() []*genai.Tool {
	osInfo := fmt.Sprintf("User's Operating System: %s (%s)", runtime.GOOS, runtime.GOARCH)
	description := fmt.Sprintf("OS specific bash execution tool. %s. Generate commands appropriate for this OS. Returns the command output or error. Never produce code, only use existing command line programs available in the system.", osInfo)

	return []*genai.Tool{{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        t.Name(),
				Description: description,
				Parameters: &genai.Schema{
					Type: genai.TypeObject,
					Properties: map[string]*genai.Schema{
						"command": {
							Type:        genai.TypeString,
							Description: "The shell command to execute (e.g., 'ls -la' for Unix/macOS, 'dir' for Windows, 'cat file.txt', etc.)",
						},
					},
					Required: []string{"command"},
				},
			},
		},
	}}
}

func (t *BashTool) HandleCall(ctx context.Context, fc *genai.FunctionCall, o agent.OutputHandler) error {
	command, _ := fc.Args["command"].(string)
	argsStruct, err := structpb.NewStruct(fc.Args)
	if err != nil {
		return err
	}
	return o(&proto.AgentOutputs{
		Contents: []*proto.Content{
			{
				Content: &proto.Content_FunctionCall{
					FunctionCall: &proto.FunctionCallContent{
						Id:   fc.ID,
						Name: fc.Name,
						Args: argsStruct,
					},
				},
			},
			{
				Content: &proto.Content_Confirmation{
					Confirmation: &proto.ConfirmationContent{
						Id:       fc.ID,
						Question: fmt.Sprintf("Can I run %q?", command),
					},
				},
			}},
	})
}

func (t *BashTool) HandleExecute(ctx context.Context, fc *genai.FunctionCall, approved bool, o agent.OutputHandler) error {
	if !approved {
		// Declined, nothing to do in terms of executing any commands.
		// But we still have to finish with a function response,
		// not to keep the previously log function call hanging forever.
		return o(&proto.AgentOutputs{
			Contents: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_FunctionResponse{
						FunctionResponse: &proto.FunctionResponseContent{
							Name: fc.Name,
							Id:   fc.ID,
						},
					},
				},
				{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "Okay.",
						},
					},
				},
			},
		})
	}

	output, err := execute(fc.Args)
	if err != nil {
		return err
	}
	respStruct, err := structpb.NewStruct(map[string]any{"result": output})
	if err != nil {
		return fmt.Errorf("failed to convert function response to structpb: %w", err)
	}
	return o(&proto.AgentOutputs{
		Contents: []*proto.Content{
			{
				Role: "assistant",
				Content: &proto.Content_FunctionResponse{
					FunctionResponse: &proto.FunctionResponseContent{
						Name:     fc.Name,
						Response: respStruct,
						Id:       fc.ID,
					},
				},
			},
			{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: output,
					},
				},
			},
		},
	})
}

type SkillsTool struct {
	executor *skills.Executor
}

func NewSkillsTool(dir string) (*SkillsTool, error) {
	executor, err := skills.NewExecutor(dir)
	if err != nil {
		return nil, err
	}
	if !executor.HasSkills() {
		return nil, nil
	}
	return &SkillsTool{executor: executor}, nil
}

func (t *SkillsTool) Name() string {
	return ""
}

func (t *SkillsTool) SystemPrompt() string {
	return t.executor.SystemPrompt()
}

func (t *SkillsTool) FuncDecl() []*genai.Tool {
	if !t.executor.HasSkills() {
		return []*genai.Tool{}
	}
	return []*genai.Tool{skills.BuildTool(t.executor.SkillNames())}
}

func (t *SkillsTool) HandleCall(ctx context.Context, fc *genai.FunctionCall, o agent.OutputHandler) error {
	argsStruct, err := structpb.NewStruct(fc.Args)
	if err != nil {
		return err
	}
	if fc.Name == "run_skill_script" {
		skill, _ := fc.Args["skill"].(string)
		script, _ := fc.Args["script"].(string)
		question := fmt.Sprintf("Can I run script %q from skill %q?", script, skill)

		return o(&proto.AgentOutputs{
			Contents: []*proto.Content{
				{
					Content: &proto.Content_FunctionCall{
						FunctionCall: &proto.FunctionCallContent{
							Id:   fc.ID,
							Name: fc.Name,
							Args: argsStruct,
						},
					},
				},
				{
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id:       fc.ID,
							Question: question,
						},
					},
				}},
		})
	}
	return o(&proto.AgentOutputs{
		Contents: []*proto.Content{
			{
				Content: &proto.Content_FunctionCall{
					FunctionCall: &proto.FunctionCallContent{
						Id:   fc.ID,
						Name: fc.Name,
						Args: argsStruct,
					},
				},
			}},
	})
}

func (t *SkillsTool) HandleExecute(ctx context.Context, fc *genai.FunctionCall, approved bool, o agent.OutputHandler) error {
	if !approved {
		// Declined, nothing to do in terms of executing any commands.
		// But we still have to finish with a function response,
		// not to keep the previously log function call hanging forever.
		return o(&proto.AgentOutputs{
			Contents: []*proto.Content{
				{
					Role: "assistant",
					Content: &proto.Content_FunctionResponse{
						FunctionResponse: &proto.FunctionResponseContent{
							Name: fc.Name,
							Id:   fc.ID,
						},
					},
				},
				{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "Okay.",
						},
					},
				},
			},
		})
	}

	var output string
	if fc.Name == "run_skill_script" {
		result := t.executor.HandleCall(ctx, fc)
		if result != nil && result.FunctionResponse != nil {
			resultMap := result.FunctionResponse.Response
			if body, ok := resultMap["instructions"]; ok {
				output = body.(string)
			} else if errStr, ok := resultMap["error"]; ok {
				output = "Error: " + errStr.(string)
			} else {
				if so, ok := resultMap["stdout"].(string); ok && so != "" {
					output += so
				}
				if se, ok := resultMap["stderr"].(string); ok && se != "" {
					if output != "" {
						output += "\n"
					}
					output += "Stderr: " + se
				}
			}
		} else {
			output = "Error: nil response from executor"
		}
	}

	respStruct, err := structpb.NewStruct(map[string]any{"result": output})
	if err != nil {
		return fmt.Errorf("failed to convert function response to structpb: %w", err)
	}
	return o(&proto.AgentOutputs{
		Contents: []*proto.Content{
			{
				Role: "assistant",
				Content: &proto.Content_FunctionResponse{
					FunctionResponse: &proto.FunctionResponseContent{
						Name:     fc.Name,
						Response: respStruct,
						Id:       fc.ID,
					},
				},
			},
			{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: output,
					},
				},
			},
		},
	})
}

type NoopTool struct {
}

func (t *NoopTool) Name() string {
	return ""
}

func (t *NoopTool) FuncDecl() []*genai.Tool {
	return []*genai.Tool{}
}

func (t *NoopTool) HandleCall(ctx context.Context, fc *genai.FunctionCall, o agent.OutputHandler) error {
	return errors.New("cannot call noop tool")
}

func (t *NoopTool) HandleExecute(ctx context.Context, fc *genai.FunctionCall, approved bool, o agent.OutputHandler) error {
	return errors.New("cannot execute noop tool")
}

func execute(args map[string]any) (string, error) {
	command, ok := args["command"].(string)
	if !ok {
		return "", fmt.Errorf("command parameter missing or invalid")
	}

	// Execute the command.
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}

	output, err := cmd.CombinedOutput()
	if err != nil {
		// Return both the error and any output that was produced
		return fmt.Sprintf("Error: %v\nOutput: %s\n\n", err, output), nil
	}

	result := strings.TrimSpace(string(output))
	if result == "" {
		return "Command executed successfully (no output)", nil
	}
	result = strings.TrimSpace(result) + "\n"

	return result, nil
}
