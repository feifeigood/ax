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
	"fmt"
	"os"

	"github.com/golang/protobuf/ptypes/duration"
	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	GeminiConfig *proto.GeminiConfig
	SkillsDir    string // Directory for discovering skills (optional)
	MaxSteps     int    // Max steps (default: 100)
}

// geminiPlannerAgent implements task.Agent using Gemini.
type geminiPlannerAgent struct {
	config     GeminiPlannerConfig
	client     *genai.Client
	bashTool   Tool
	skillsTool Tool
	registry   *Registry
}

// NewGeminiPlannerAgent creates a new Gemini-based agent.
func NewGeminiPlannerAgent(ctx context.Context, registry *Registry, config GeminiPlannerConfig) (agent.Agent, error) {
	if config.GeminiConfig == nil {
		config.GeminiConfig = &proto.GeminiConfig{}
	}
	if config.GeminiConfig.Timeout == nil {
		config.GeminiConfig.Timeout = &duration.Duration{Seconds: 30}
	}
	if config.GeminiConfig.Model == "" {
		config.GeminiConfig.Model = os.Getenv("AX_GEMINI_MODEL")
		if config.GeminiConfig.Model == "" {
			config.GeminiConfig.Model = "gemini-3-flash-preview"
		}
	}

	// Default system prompt
	if config.GeminiConfig.SystemPrompt == "" {
		config.GeminiConfig.SystemPrompt = `You are an intelligent orchestrator. Your role is to analyze the conversation history and user requests, then select the most appropriate agent to handle the task.

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
		APIKey: os.Getenv("GEMINI_API_KEY"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// TODO(jbd): Enable skills tool.
	return &geminiPlannerAgent{
		client:   client,
		bashTool: &BashTool{},
		registry: registry,
		config:   config,
	}, nil
}

func (p *geminiPlannerAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	var outputs []*proto.Content
	var outputCapturer = func(resp *proto.AgentOutputs) error {
		outputs = append(outputs, resp.Contents...)
		return handler(resp)
	}
	nextAgentID, err := p.process(ctx, start, outputCapturer)
	if err != nil {
		return err
	}
	if nextAgentID == "" {
		return nil
	}
	if err := e.Exec(ctx, nextAgentID, &proto.AgentStart{
		AgentId:  nextAgentID,
		Contents: append(start.Contents, outputs...),
	}, handler); err != nil {
		return err
	}
	return nil
}

func (p *geminiPlannerAgent) HealthCheck(ctx context.Context) error {
	return nil
}

func (p *geminiPlannerAgent) Close() error {
	return nil
}

func (p *geminiPlannerAgent) process(ctx context.Context, start *proto.AgentStart, handler agent.OutputHandler) (agentID string, err error) {
	tools, err := agentsToTools(p.registry, p.bashTool, p.skillsTool)
	if err != nil {
		return "", fmt.Errorf("failed to convert agents to tools: %w", err)
	}

	inputs := start.Contents
	if fc, approved := p.handleConfirmationAnswer(inputs); fc != nil {
		if p.bashTool.Name() == fc.Name {
			return "", p.bashTool.HandleExecute(ctx, fc, approved, handler)
		}
	}

	contents := protoToContents(inputs)
	ctx, cancel := context.WithTimeout(ctx, p.config.GeminiConfig.Timeout.AsDuration())
	defer cancel()

	resp, err := p.client.Models.GenerateContent(ctx, p.config.GeminiConfig.Model, contents, &genai.GenerateContentConfig{
		Tools: tools,
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			},
		},
		SystemInstruction: genai.Text(p.config.GeminiConfig.SystemPrompt)[0],
		MaxOutputTokens:   p.config.GeminiConfig.MaxTokens,
		CandidateCount:    1,
	})

	if err != nil {
		return "", fmt.Errorf("failed to generate in planner: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return "", fmt.Errorf("no candidates from Gemini in planner")
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil || candidate.Content.Parts == nil {
		if candidate.FinishReason == genai.FinishReasonStop {
			return "", nil // No more tasks
		}
		return "", fmt.Errorf("no content in candidates from Gemini")
	}

	// Look for function calls in the response
	for _, part := range candidate.Content.Parts {
		if part == nil {
			continue
		}

		if part.Text != "" {
			if err := handler(&proto.AgentOutputs{
				Contents: []*proto.Content{{
					Role: "assistant",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{Text: part.Text},
					},
				}},
			}); err != nil {
				return "", err
			}
		}

		if fc := part.FunctionCall; fc != nil {
			fc.ID = uuid.NewString()
			switch fc.Name {
			case p.bashTool.Name():
				return "", p.bashTool.HandleCall(ctx, fc, handler)
			case "run_skill_script", "activate_skill":
				return "", p.skillsTool.HandleCall(ctx, fc, handler)
			default:
				return fc.Name, nil
			}
		}
	}
	return "", nil
}

func (p *geminiPlannerAgent) handleConfirmationAnswer(inputs []*proto.Content) (*genai.FunctionCall, bool) {
	var conf *proto.ConfirmationContent
	var approved bool
	for _, input := range inputs {
		if input.GetConfirmation() != nil && input.GetConfirmation().GetApproval() != nil {
			conf = input.GetConfirmation()
			approved = true
		}
		if input.GetConfirmation() != nil && input.GetConfirmation().GetDecline() != nil {
			conf = input.GetConfirmation()
			approved = false
		}
	}
	if conf == nil {
		return nil, false
	}

	var fc *genai.FunctionCall
	for _, input := range inputs {
		if input.GetFunctionCall() == nil {
			continue
		}
		if fn := input.GetFunctionCall(); fn != nil && fn.Id == conf.Id {
			fc = &genai.FunctionCall{
				ID:   conf.Id,
				Name: fn.Name,
				Args: fn.Args.AsMap(),
			}
			break
		}
	}

	if fc == nil {
		return nil, false
	}

	// Ensure that we don't have a response for the function call.
	// Otherwise, we will execute the function call forever.
	for _, input := range inputs {
		if input.GetFunctionResponse() == nil {
			continue
		}
		if fr := input.GetFunctionResponse(); fr != nil && fr.Id == fc.ID {
			// We executed this previously.
			// There is nothing more to execute.
			return nil, false
		}
	}
	return fc, approved
}

// agentsToTools converts registry agents to Gemini function declarations.
func agentsToTools(registry *Registry, nativeTools ...Tool) ([]*genai.Tool, error) {
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
	for _, nativeTool := range nativeTools {
		if nativeTool == nil {
			continue
		}
		tools = append(tools, nativeTool.FuncDecl()...)
	}
	return tools, nil
}

// protoToContents converts history to Gemini conversation format.
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
			switch m.Confirmation.Decision.(type) {
			case *proto.ConfirmationContent_Decline:
				// shouldn't be sent to Gemini
			case *proto.ConfirmationContent_Approval:
				// shouldn't be sent to Gemini
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
