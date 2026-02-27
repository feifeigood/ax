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
	"io"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/skills"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"google.golang.org/genai"
	"google.golang.org/protobuf/types/known/structpb"
)

const bashToolName = "bash"

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	APIKey       string        // Google AI API key (for programmatic use only; if empty, uses GEMINI_API_KEY env var - recommended)
	Model        string        // Model name (default: gemini-3-flash-preview)
	MaxTokens    int32         // Max output tokens (default: 8192)
	Timeout      time.Duration // Request timeout (default: 60s)
	SystemPrompt string        // Custom system prompt (optional)
	SkillsDir    string        // Directory for discovering skills (optional)
}

func NewGeminiPlanner(ctx context.Context, registry *Registry, config GeminiPlannerConfig) (agent.Agent, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Model == "" {
		config.Model = os.Getenv("GAR_GEMINI_MODEL")
		if config.Model == "" {
			config.Model = "gemini-3-flash-preview"
		}
	}
	if config.APIKey == "" {
		config.APIKey = os.Getenv("GEMINI_API_KEY")
		if config.APIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set and no API key provided in config")
		}
	}

	// Default system prompt
	if config.SystemPrompt == "" {
		config.SystemPrompt = `You are an intelligent orchestrator. Your role is to analyze the conversation history and user requests, then select the most appropriate agent to handle the task.

Available tools have been provided to you as function tools. Each agent has:
- A unique ID
- A description of its capabilities

Your job is to:
1. Analyze the current conversation context and understand what needs to be done
2. Select the best tool for the task by calling the appropriate function
3. If enough work is done, stop to indicate completion

Guidelines:
- Choose tools based on their capabilities and the user's needs.
- Keep responses concise, don't chat too much about what you can do.
- If no suitable tool exists, stop.
- Keep the conversation context in mind when selecting tools.
- It's valid not to choose a tool.
- Once something is approved, try executing it.`
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: config.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	skillsDir := config.SkillsDir
	if skillsDir == "" {
		skillsDir = os.Getenv("SKILLS_DIR")
	}
	if skillsDir == "" {
		skillsDir = skills.DefaultDir()
	}

	skillExec, err := skills.NewExecutor(client, config.Model, skillsDir)
	if err != nil && err != io.EOF {
		log.Printf("Warning: failed to initialize skill executor for %q: %v\n", skillsDir, err)
	}

	if skillExec != nil && skillExec.HasSkills() {
		config.SystemPrompt += "\n\n" + skillExec.SystemPrompt()

		skillExec.OnUpdate(func(ev skills.UpdateEvent) {
			// No-op for cleaner CLI logs
		})
	}

	return &geminiPlannerAgent{
		client:    client,
		bashTool:  newBashTool(),
		registry:  registry,
		config:    config,
		skillExec: skillExec,
	}, nil
}

type geminiPlannerAgent struct {
	client    *genai.Client
	bashTool  *bashTool
	registry  *Registry
	config    GeminiPlannerConfig
	skillExec *skills.Executor
}

// agentsToTools converts registry agents to Gemini function declarations.
func agentsToTools(bashTool *bashTool, registry *Registry) ([]*genai.Tool, error) {
	healthyAgents := registry.ListHealthy()

	var tools []*genai.Tool
	// TODO(lhuan): Check if agentsToTools returns an error or empty list and return a friendly "no agent available, try later" error.
	for _, id := range healthyAgents {
		info, err := registry.GetInfo(id)
		if err != nil {
			continue // Skip agents we can't get info for
		}

		// Create a function declaration for this agent
		funcDecl := &genai.FunctionDeclaration{
			Name:        id, // Use agent ID as function name
			Description: fmt.Sprintf("%s, %s", info.Name, info.Description),
		}

		tools = append(tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{funcDecl},
		})
	}
	tools = append(tools, bashTool.funcDecl())
	return tools, nil
}

func (p *geminiPlannerAgent) Process(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	// Convert agents to Gemini function declarations
	tools, err := agentsToTools(p.bashTool, p.registry)
	if err != nil {
		return fmt.Errorf("failed to convert agents to tools: %w", err)
	}
	if p.skillExec != nil && p.skillExec.HasSkills() {
		tools = append(tools, skills.BuildTool(p.skillExec.SkillNames()))
	}

	// Convert session to conversation history
	contents := protoToContents(incoming.Contents)
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()

	resp, err := p.client.Models.GenerateContent(ctx, p.config.Model, contents, &genai.GenerateContentConfig{
		Tools: tools,
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			},
		},
		SystemInstruction: genai.Text(p.config.SystemPrompt)[0],
		MaxOutputTokens:   p.config.MaxTokens,
		CandidateCount:    1,
	})

	if err != nil {
		return fmt.Errorf("failed to generate in planner: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return fmt.Errorf("no candidates from Gemini in planner")
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil || candidate.Content.Parts == nil {
		if candidate.FinishReason == genai.FinishReasonStop {
			return nil // No more tasks
		}
		return fmt.Errorf("no content in candidates from Gemini")
	}

	// Look for function calls in the response
	for _, part := range candidate.Content.Parts {
		if part == nil {
			continue
		}

		if part.Text != "" {
			if err := handler(&proto.ProcessResponse{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{Text: part.Text},
					},
				}},
			}); err != nil {
				return err
			}
		}

		if fc := part.FunctionCall; fc != nil {
			argsStruct, err := structpb.NewStruct(fc.Args)
			if err != nil {
				return fmt.Errorf("failed to convert function call args to structpb: %w", err)
			}
			if err := handler(&proto.ProcessResponse{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_FunctionCall{
						FunctionCall: &proto.FunctionCallContent{
							Name:             fc.Name,
							Args:             argsStruct,
							Id:               fc.ID,
							ThoughtSignature: part.ThoughtSignature,
						},
					},
				}},
			}); err != nil {
				return err
			}

			switch fc.Name {
			case "activate_skill", "run_skill_script":
				if p.skillExec == nil {
					return fmt.Errorf("skill executor not initialized")
				}

				if fc.Name == "run_skill_script" {
					skill, _ := fc.Args["skill"].(string)
					script, _ := fc.Args["script"].(string)
					question := fmt.Sprintf("Can I run script %q from skill %q?", script, skill)

					continueProcessing, err := checkCommandApproval(incoming.Contents, question, handler)
					if err != nil {
						return err
					}
					if !continueProcessing {
						return nil
					}
				}

				resultPart := p.skillExec.HandleCall(ctx, fc)

				// Format the output for GAR's text-based history log
				var output string
				if resultPart != nil && resultPart.FunctionResponse != nil {
					resultMap := resultPart.FunctionResponse.Response
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
						if output == "" {
							output = "Command executed successfully (no output)"
						}
					}
				} else {
					output = "Error: nil response from executor"
				}

				// Explicitly format as a tool result so the reconstructed history doesn't confuse the model
				respStruct, err := structpb.NewStruct(map[string]interface{}{"result": output})
				if err != nil {
					return fmt.Errorf("failed to convert function response to structpb: %w", err)
				}

				return handler(&proto.ProcessResponse{
					Contents: []*proto.Content{{
						Role: "assistant",
						Content: &proto.Content_FunctionResponse{
							FunctionResponse: &proto.FunctionResponseContent{
								Name:     fc.Name,
								Response: respStruct,
								Id:       fc.ID,
							},
						},
					}},
					AgentHandoff: plannerAgentID,
				})

			case bashToolName:
				command, ok := fc.Args["command"].(string)
				if !ok {
					return fmt.Errorf("command parameter missing or invalid in function call")
				}
				question := fmt.Sprintf("Can I execute %q?", command)

				continueProcessing, err := checkCommandApproval(incoming.Contents, question, handler)
				if err != nil {
					return err
				}
				if !continueProcessing {
					return nil
				}

				output, err := p.bashTool.executeShellCommand(fc.Args)
				if err != nil {
					return err
				}

				respStruct, err := structpb.NewStruct(map[string]interface{}{"result": output})
				if err != nil {
					return fmt.Errorf("failed to convert function response to structpb: %w", err)
				}

				return handler(&proto.ProcessResponse{
					Contents: []*proto.Content{{
						Role: "assistant", // Note: The history uses assistant for now
						Content: &proto.Content_FunctionResponse{
							FunctionResponse: &proto.FunctionResponseContent{
								Name:     fc.Name,
								Response: respStruct,
								Id:       fc.ID,
							},
						},
					}},
					AgentHandoff: plannerAgentID, // Explicitly return to planner in same response
				})
			default:
				return handler(&proto.ProcessResponse{
					AgentHandoff: fc.Name,
				})
			}
		}
	}
	return nil
}

// HealthCheck checks if the agent is healthy and responsive.
// Returns an error if the agent is unhealthy or unreachable.
func (p *geminiPlannerAgent) HealthCheck(ctx context.Context) error {
	return nil // always healthy
}

// Close gracefully shuts down the agent and releases resources.
func (p *geminiPlannerAgent) Close() error {
	return nil
}

// protoToContents converts session message history to Gemini conversation format.
func protoToContents(inputs []*proto.Content) []*genai.Content {
	var contents []*genai.Content

	// Convert each message to Gemini format
	for _, msg := range inputs {
		role := msg.Role
		if role != "user" {
			role = "model"
		}

		switch m := msg.Content.(type) {
		case *proto.Content_Text:
			contents = append(contents, &genai.Content{
				Role: role,
				Parts: []*genai.Part{
					{
						Text: m.Text.Text,
					},
				},
			})
		case *proto.Content_Confirmation:
			if m.Confirmation.Question != "" {
				contents = append(contents, &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{Text: m.Confirmation.Question},
					},
				})
			}
			switch d := m.Confirmation.Decision.(type) {
			case *proto.ConfirmationContent_Decline:
				// should never happen
			case *proto.ConfirmationContent_Approval:
				if d.Approval.Approved {
					contents = append(contents, &genai.Content{
						Role: "user",
						Parts: []*genai.Part{
							{Text: "Approved."},
						},
					})
				}
			}
		case *proto.Content_FunctionCall:
			contents = append(contents, &genai.Content{
				Role: "model",
				Parts: []*genai.Part{
					{
						ThoughtSignature: m.FunctionCall.ThoughtSignature,
						FunctionCall: &genai.FunctionCall{
							ID:   m.FunctionCall.Id,
							Name: m.FunctionCall.Name,
							Args: m.FunctionCall.Args.AsMap(),
						},
					},
				},
			})
		case *proto.Content_FunctionResponse:
			contents = append(contents, &genai.Content{
				Role: "user",
				Parts: []*genai.Part{
					{
						FunctionResponse: &genai.FunctionResponse{
							ID:       m.FunctionResponse.Id,
							Name:     m.FunctionResponse.Name,
							Response: m.FunctionResponse.Response.AsMap(),
						},
					},
				},
			})
		}
		// TODO(jbd): Handle other content types (e.g., images, files)
	}
	return contents
}

func newBashTool() *bashTool {
	return &bashTool{}
}

// bashTool is the built-in tool that allows
// planner to execute general purpose bash commands.
type bashTool struct {
}

func (f *bashTool) funcDecl() *genai.Tool {
	osInfo := fmt.Sprintf("User's Operating System: %s (%s)", runtime.GOOS, runtime.GOARCH)
	description := fmt.Sprintf("OS specific bash execution tool. %s. Generate commands appropriate for this OS. Returns the command output or error. Never produce code, only use existing command line programs available in the system.", osInfo)

	return &genai.Tool{
		FunctionDeclarations: []*genai.FunctionDeclaration{
			{
				Name:        bashToolName,
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
	}
}

func (f *bashTool) executeShellCommand(args map[string]any) (string, error) {
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
	result += "\n\n"

	return result, nil
}

// checkCommandApproval checks if the user has approved or declined the command.
// It searches backwards through the history for a confirmation request with the same question
// and then looks for the user's response to that confirmation.
// This isn't a true implementation based on id checking, but it's good enough for now.
func checkCommandApproval(history []*proto.Content, question string, handler agent.OutputHandler) (cont bool, err error) {
	// Find the most recent confirmation request testing this question
	var expectedID string
	for i := len(history) - 1; i >= 0; i-- {
		c := history[i]
		if conf := c.GetConfirmation(); conf != nil && c.Role == "assistant" && conf.Question == question {
			expectedID = conf.Id
			break
		}
	}

	if expectedID != "" {
		// Find the user's response to this confirmation ID
		for i := len(history) - 1; i >= 0; i-- {
			c := history[i]
			if conf := c.GetConfirmation(); conf != nil && c.Role == "user" && conf.Id == expectedID {
				if approval := conf.GetApproval(); approval != nil {
					if approval.Approved {
						return true, nil
					}
				}
				return false, errors.New("commands must be already approved before geminiPlannerAgent.Process")
			}
		}
	}

	// Not decided yet, prompt the user for confirmation.
	err = handler(&proto.ProcessResponse{
		Contents: []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id:       uuid.New().String(),
					Question: question,
				},
			},
		}},
	})
	return false, err
}
