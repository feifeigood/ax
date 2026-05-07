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

package gemini

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
	"google.golang.org/genai"
)

// AgentRegistry defines the interface needed by the planner to discover agents.
type AgentRegistry interface {
	List() []string
	GetInfo(id string) (*agent.AgentInfo, error)
}

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	GeminiConfig *config.GeminiConfig
	SkillsDir    string // Directory for discovering skills (optional)
}

// geminiPlannerAgent implements task.Agent using Gemini.
type geminiPlannerAgent struct {
	config     GeminiPlannerConfig
	client     *genai.Client
	bashTool   Tool
	skillsTool Tool
	registry   AgentRegistry
}

// NewGeminiPlannerAgent creates a new Gemini-based agent.
func NewGeminiPlannerAgent(ctx context.Context, registry AgentRegistry, cfg GeminiPlannerConfig) (agent.Agent, error) {
	if cfg.GeminiConfig == nil {
		cfg.GeminiConfig = &config.GeminiConfig{}
	}
	if cfg.GeminiConfig.Timeout == 0 {
		cfg.GeminiConfig.Timeout = 30 * time.Second
	}
	if cfg.GeminiConfig.Model == "" {
		cfg.GeminiConfig.Model = os.Getenv("AX_GEMINI_MODEL")
		if cfg.GeminiConfig.Model == "" {
			cfg.GeminiConfig.Model = "gemini-3-flash-preview"
		}
	}

	// Default system prompt
	if cfg.GeminiConfig.SystemPrompt == "" {
		cfg.GeminiConfig.SystemPrompt = `You are the Primary Architect and Executor Agent in the AX system. Your goal is to solve the user's request as thoroughly and efficiently as possible.

You have two primary ways to accomplish tasks:
1. **Direct Execution**: You have access to a 'bash' tool to run shell commands on the system. Use this to perform work directly (e.g., creating files, running tests, searching).
2. **Delegation**: You have access to specialized subagents (registered as function tools). Review their descriptions and delegate tasks to them if they are better suited for the job.

Rules for Operation:
- **MANDATORY FIRST STEP**: You MUST first check if any available subagent is specialized for the user's request. If a specialized subagent exists, you MUST call that subagent. Do not attempt the task yourself if a subagent can handle it.
- If no subagent is specialized, you must attempt to solve the task yourself using the 'bash' tool. Do not give up; use commands to explore, create, and verify.
- **Clarification**: If the user's request is ambiguous or you are unclear about any requirements, stop and ask the user for clarification before proceeding with any tool calls or delegation.
- Always be concise. Focus on action and execution results rather than conversational explanations.`
	}

	// Fail fast if no Gemini credentials are configured. We check the three
	// env vars the underlying genai SDK recognizes (see
	// google.golang.org/genai/client.go NewClient docs):
	//   - GEMINI_API_KEY: AI Studio API key.
	//   - GOOGLE_API_KEY: alternate name for the AI Studio API key; the SDK
	//     accepts both and gives this one precedence when both are set.
	//   - GOOGLE_GENAI_USE_VERTEXAI: switches the SDK to the Vertex AI
	//     backend, which uses Application Default Credentials instead of an
	//     API key.
	if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" && os.Getenv("GOOGLE_GENAI_USE_VERTEXAI") == "" {
		return nil, fmt.Errorf("no Gemini credentials configured: set either GEMINI_API_KEY (AI Studio) " +
			"or GOOGLE_GENAI_USE_VERTEXAI=True with GCLOUD_PROJECT and GCLOUD_LOCATION (Vertex AI) " +
			"on the ax serve process and restart it; " +
			"see https://github.com/google-gemini/ax#authentication")
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// NewSkillsTool already converts skills.ErrNoSkills (empty dir)
	// into a NoopTool with a nil error, so any error here is real.
	skillsTool, err := NewSkillsTool(cfg.SkillsDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize skills tool: %w", err)
	}

	p := &geminiPlannerAgent{
		client:     client,
		bashTool:   &BashTool{},
		skillsTool: skillsTool,
		registry:   registry,
		config:     cfg,
	}

	if sp := skillsTool.SystemPrompt(); sp != "" {
		p.config.GeminiConfig.SystemPrompt += "\n\n" + sp
	}
	return p, nil
}

func (p *geminiPlannerAgent) Connect(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	return p.loop(ctx, conversationID, start, e, handler)
}

func (p *geminiPlannerAgent) Close() error {
	return nil
}

func (p *geminiPlannerAgent) loop(ctx context.Context, conversationID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) (err error) {
	var outputs []*proto.Message
	var outputCapturer = func(resp *proto.AgentOutputs) error {
		outputs = append(outputs, resp.Messages...)
		return handler(resp)
	}

	for {
		nextAgentID, keepLooping, err := p.process(ctx, start, outputCapturer)
		if err != nil {
			return err
		}
		if keepLooping {
			// Some function calls require multiple turns to complete,
			// e.g. the skill activation and running.
			// Allow the loop to continue without switching agents or user input.
			start.Messages = append(start.Messages, outputs...)
			outputs = nil
			continue
		}

		if nextAgentID == "" {
			// No agent to delegate, we are done.
			return nil
		}
		start = &proto.AgentStart{
			AgentId:  nextAgentID,
			Messages: append(start.Messages, outputs...),
		}
		outputs = nil
		if _, err := e.Exec(ctx, conversationID, nextAgentID, start, outputCapturer); err != nil {
			return err
		}
	}
}

func (p *geminiPlannerAgent) process(ctx context.Context, start *proto.AgentStart, handler agent.OutputHandler) (agentID string, keepLooping bool, err error) {
	tools, err := agentsToTools(p.registry, p.bashTool, p.skillsTool)
	if err != nil {
		return "", false, fmt.Errorf("failed to convert agents to tools: %w", err)
	}

	inputs := start.Messages
	if fc, approved := p.handleConfirmationAnswer(inputs); fc != nil {
		if fc.Name == p.bashTool.Name() {
			return "", false, p.bashTool.HandleExecute(ctx, fc, approved, handler)
		}
		if fc.Name == "run_skill_script" {
			return "", false, p.skillsTool.HandleExecute(ctx, fc, approved, handler)
		}
	}

	contents := protoToContents(inputs)
	ctx, cancel := context.WithTimeout(ctx, p.config.GeminiConfig.Timeout)
	defer cancel()

	genCfg := &genai.GenerateContentConfig{
		Tools: tools,
		ToolConfig: &genai.ToolConfig{
			FunctionCallingConfig: &genai.FunctionCallingConfig{
				Mode: genai.FunctionCallingConfigModeAuto,
			},
		},
		SystemInstruction: genai.Text(p.config.GeminiConfig.SystemPrompt)[0],
		MaxOutputTokens:   p.config.GeminiConfig.MaxTokens,
		CandidateCount:    1,
	}
	// Temperature is *float32 in genai: leave nil to inherit the model's
	// default. We treat the zero value in our config the same way.
	if t := p.config.GeminiConfig.Temperature; t > 0 {
		genCfg.Temperature = &t
	}

	resp, err := p.client.Models.GenerateContent(ctx, p.config.GeminiConfig.Model, contents, genCfg)

	if err != nil {
		return "", false, fmt.Errorf("failed to generate in planner: %w", err)
	}
	if len(resp.Candidates) == 0 {
		return "", false, fmt.Errorf("no candidates from Gemini in planner")
	}
	candidate := resp.Candidates[0]
	if candidate.Content == nil || candidate.Content.Parts == nil {
		if candidate.FinishReason == genai.FinishReasonStop {
			return "", false, nil // No more tasks
		}
		return "", false, fmt.Errorf("no content in candidates from Gemini")
	}

	// Look for function calls in the response
	for _, part := range candidate.Content.Parts {
		if part == nil {
			continue
		}

		if part.Text != "" {
			if err := handler(&proto.AgentOutputs{
				Messages: []*proto.Message{{
					Role: "model",
					Content: &proto.Content{
						Type: &proto.Content_Text{
							Text: &proto.TextContent{Text: part.Text},
						},
					},
				}},
			}); err != nil {
				return "", false, err
			}
		}

		if fc := part.FunctionCall; fc != nil {
			// Preserve Gemini's function-call ID when the model assigns
			// one so it round-trips correctly in subsequent turns. Only
			// mint a UUID when the model leaves it empty.
			if fc.ID == "" {
				fc.ID = uuid.NewString()
			}
			switch fc.Name {
			case p.bashTool.Name():
				return "", false, p.bashTool.HandleCall(ctx, fc, handler)
			case "run_skill_script":
				return "", false, p.skillsTool.HandleCall(ctx, fc, handler)
			case "activate_skill":
				return "", true, p.skillsTool.HandleCall(ctx, fc, handler)
			default:
				return fc.Name, false, nil
			}
		}
	}
	return "", false, nil
}

func (p *geminiPlannerAgent) handleConfirmationAnswer(inputs []*proto.Message) (*genai.FunctionCall, bool) {
	var conf *proto.ConfirmationContent
	var approved bool
	for _, input := range inputs {
		content := input.GetContent()
		if content.GetConfirmation() != nil && content.GetConfirmation().GetApproval() != nil {
			conf = content.GetConfirmation()
			approved = true
		}
		if content.GetConfirmation() != nil && content.GetConfirmation().GetDecline() != nil {
			conf = content.GetConfirmation()
			approved = false
		}
	}
	if conf == nil {
		return nil, false
	}

	var fc *genai.FunctionCall
	for _, input := range inputs {
		content := input.GetContent()
		tc := content.GetToolCall()
		if tc == nil || tc.GetFunctionCall() == nil {
			continue
		}
		if tc.Id == conf.Id {
			fn := tc.GetFunctionCall()
			fc = &genai.FunctionCall{
				ID:   conf.Id,
				Name: fn.Name,
				Args: fn.Arguments.AsMap(),
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
		content := input.GetContent()
		tr := content.GetToolResult()
		if tr == nil || tr.GetFunctionResult() == nil {
			continue
		}
		if tr.CallId == fc.ID {
			// We executed this previously.
			// There is nothing more to execute.
			return nil, false
		}
	}
	return fc, approved
}

// agentsToTools converts registry agents to Gemini function declarations.
func agentsToTools(registry AgentRegistry, nativeTools ...Tool) ([]*genai.Tool, error) {
	agents := registry.List()

	var tools []*genai.Tool
	// TODO(lhuan): Check if agentsToTools returns an error or empty list and return a friendly "no agent available, try later" error.
	for _, id := range agents {
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
func protoToContents(inputs []*proto.Message) []*genai.Content {
	var contents []*genai.Content

	// Convert each message to Gemini format
	for _, msg := range inputs {
		// Skip internal messages.
		if msg.GetInternalOnly() {
			continue
		}
		role := msg.Role
		if role != "user" {
			role = "model"
		}

		content := msg.GetContent()
		if content == nil {
			continue
		}

		switch m := content.Type.(type) {
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
			// shouldn't be sent to Gemini
			switch m.Confirmation.Decision.(type) {
			case *proto.ConfirmationContent_Decline:
				// shouldn't be sent to Gemini
			case *proto.ConfirmationContent_Approval:
				// shouldn't be sent to Gemini
			}
		case *proto.Content_ToolCall:
			tc := m.ToolCall
			if fc := tc.GetFunctionCall(); fc != nil {
				contents = append(contents, &genai.Content{
					Role: "model",
					Parts: []*genai.Part{
						{
							ThoughtSignature: tc.Signature,
							FunctionCall: &genai.FunctionCall{
								ID:   tc.Id,
								Name: fc.Name,
								Args: fc.Arguments.AsMap(),
							},
						},
					},
				})
			}
		case *proto.Content_ToolResult:
			tr := m.ToolResult
			if fr := tr.GetFunctionResult(); fr != nil {
				var respMap map[string]any
				if fr.GetResponse() != nil {
					respMap = fr.GetResponse().AsMap()
				}
				contents = append(contents, &genai.Content{
					Role: "user",
					Parts: []*genai.Part{
						{
							FunctionResponse: &genai.FunctionResponse{
								ID:       tr.CallId,
								Name:     fr.Name,
								Response: respMap,
							},
						},
					},
				})
			}
		}
		// TODO(jbd): Handle other content types (e.g., images, files)
	}
	return contents
}
