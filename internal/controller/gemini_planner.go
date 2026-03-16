package controller

import (
	"context"
	"fmt"
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

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	GeminiConfig GeminiConfig
	SkillsDir    string // Directory for discovering skills (optional)
	MaxSteps     int    // Max steps (default: 100)
}

// geminiPlannerAgent implements task.Agent using Gemini.
type geminiPlannerAgent struct {
	config    GeminiPlannerConfig
	client    *genai.Client
	bashTool  *bashTool
	registry  *Registry
	skillExec *skills.Executor
}

// NewGeminiPlannerAgent creates a new Gemini-based agent.
func NewGeminiPlannerAgent(ctx context.Context, registry *Registry, config GeminiPlannerConfig) (agent.Agent, error) {
	if config.GeminiConfig.Timeout == 0 {
		config.GeminiConfig.Timeout = 30 * time.Second
	}
	if config.GeminiConfig.Model == "" {
		config.GeminiConfig.Model = os.Getenv("GAR_GEMINI_MODEL")
		if config.GeminiConfig.Model == "" {
			config.GeminiConfig.Model = "gemini-3-flash-preview"
		}
	}
	if config.GeminiConfig.APIKey == "" {
		config.GeminiConfig.APIKey = os.Getenv("GEMINI_API_KEY")
		if config.GeminiConfig.APIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set and no API key provided in config")
		}
	}

	if config.MaxSteps == 0 {
		config.MaxSteps = 5
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
		APIKey: config.GeminiConfig.APIKey,
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

	skillExec, err := skills.NewExecutor(client, config.GeminiConfig.Model, skillsDir)
	if err != nil {
		return nil, err
	}

	if skillExec.HasSkills() {
		// config.SystemPrompt += "\n\n" + skillExec.SystemPrompt()
	}
	return &geminiPlannerAgent{
		client:    client,
		bashTool:  newBashTool(),
		registry:  registry,
		config:    config,
		skillExec: skillExec,
	}, nil
}

func (p *geminiPlannerAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, handler agent.OutputHandler) error {
	var outputs []*proto.Content
	var outputCapturer = func(resp *proto.ProcessResponse) error {
		outputs = append(outputs, resp.Contents...)
		return handler(resp)
	}

	t.Inputs = append(t.Inputs, outputs...)
	nextAgentID, err := p.process(ctx, t, outputCapturer)
	if err != nil {
		return err
	}
	if nextAgentID == "" {
		return nil
	}
	if err := e.Exec(ctx, &agent.Task{
		ID:      nextAgentID,
		AgentID: nextAgentID,
		Inputs:  t.Inputs,
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

func (p *geminiPlannerAgent) process(ctx context.Context, t *agent.Task, handler agent.OutputHandler) (agentID string, err error) {
	tools, err := agentsToTools(p.bashTool, p.registry)
	if err != nil {
		return "", fmt.Errorf("failed to convert agents to tools: %w", err)
	}
	if p.skillExec.HasSkills() {
		// tools = append(tools, skills.BuildTool(p.skillExec.SkillNames()))
	}

	inputs := t.Inputs
	if fc, approved := p.handleConfirmationAnswer(inputs); fc != nil {
		return "", p.handleExecute(ctx, fc, approved, handler)
	}

	contents := protoToContents(inputs)
	ctx, cancel := context.WithTimeout(ctx, p.config.GeminiConfig.Timeout)
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
			if err := handler(&proto.ProcessResponse{
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
			case bashToolName:
				return "", p.handleBash(fc, handler)
			case "run_skill_script", "activate_skill":
				return "", p.handleSkill(ctx, fc, handler)
			default:
				return fc.Name, nil
			}
		}
	}
	return "", nil
}

func (p *geminiPlannerAgent) handleSkill(ctx context.Context, fc *genai.FunctionCall, o agent.OutputHandler) error {
	if fc.Name == "run_skill_script" {
		skill, _ := fc.Args["skill"].(string)
		script, _ := fc.Args["script"].(string)
		question := fmt.Sprintf("Can I run script %q from skill %q?", script, skill)

		argsStruct, err := structpb.NewStruct(fc.Args)
		if err != nil {
			return err
		}
		return o(&proto.ProcessResponse{
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

	resultPart := p.skillExec.HandleCall(ctx, fc)
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

	respStruct, err := structpb.NewStruct(map[string]any{"result": output})
	if err != nil {
		return fmt.Errorf("failed to convert function response to structpb: %w", err)
	}
	return o(&proto.ProcessResponse{
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
	})
}

func (p *geminiPlannerAgent) handleBash(fc *genai.FunctionCall, o agent.OutputHandler) error {
	command, _ := fc.Args["command"].(string)
	argsStruct, err := structpb.NewStruct(fc.Args)
	if err != nil {
		return err
	}
	return o(&proto.ProcessResponse{
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

func (p *geminiPlannerAgent) handleExecute(ctx context.Context, fc *genai.FunctionCall, approved bool, o agent.OutputHandler) error {
	if !approved {
		// Declined, nothing to do in terms of executing any commands.
		// But we still have to finish with a function response,
		// not to keep the previously log function call hanging forever.
		return o(&proto.ProcessResponse{
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
	switch fc.Name {
	case "bash":
		var err error
		output, err = execute(fc.Args)
		if err != nil {
			return err
		}
	case "run_skill_script":
		result := p.skillExec.HandleCall(ctx, fc)
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
	return o(&proto.ProcessResponse{
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
	if bashTool != nil {
		tools = append(tools, bashTool.funcDecl())
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
	result += "\n\n"

	return result, nil
}
