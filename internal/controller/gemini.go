package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/google/gar/proto"
	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
)

const noActionAgentID = "no_action_agent"

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	APIKey        string        // Google AI API key (for programmatic use only; if empty, uses GEMINI_API_KEY env var - recommended)
	Model         string        // Model name (default: "gemini-flash-latest", can override with GAR_GEMINI_MODEL env var)
	Temperature   float32       // Temperature for generation (default: 0.7)
	MaxTokens     int32         // Max output tokens (default: 8192)
	Timeout       time.Duration // Request timeout (default: 60s)
	SystemPrompt  string        // Custom system prompt (optional)
	ContextWindow int           // Number of recent messages to include (default: 30)
}

// NewGeminiPlanFunc creates a planning function that uses Gemini for intelligent agent selection.
// It converts available agents into function declarations and lets Gemini decide which agent
// to invoke based on the session context and agent capabilities.
func NewGeminiPlanFunc(ctx context.Context, registry *Registry, config GeminiPlannerConfig) (PlanFunc, error) {
	// Set defaults
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if config.Model == "" {
		config.Model = os.Getenv("GAR_GEMINI_MODEL")
		if config.Model == "" {
			config.Model = "gemini-flash-latest"
		}
	}
	if config.APIKey == "" {
		config.APIKey = os.Getenv("GEMINI_API_KEY")
		if config.APIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set and no API key provided in config")
		}
	}
	if config.ContextWindow == 0 {
		config.ContextWindow = 30
	}

	// Default system prompt
	if config.SystemPrompt == "" {
		config.SystemPrompt = `You are an intelligent agent orchestrator. Your role is to analyze the conversation history and user requests, then select the most appropriate agent to handle the task.

Available agents have been provided to you as function tools. Each agent has:
- A unique ID
- A name describing its purpose
- A description of its capabilities
- Metadata with additional information

Your job is to:
1. Analyze the current conversation context and understand what needs to be done
2. Select the best agent for the task by calling the appropriate function
3. Provide clear, relevant input to the selected agent

Guidelines:
- Choose agents based on their capabilities and the user's needs
- If no suitable agent exists, call no_action to indicate completion
- Keep the conversation context in mind when selecting agents
- Provide concise but complete input to the selected agent`
	}

	// Create Gemini client
	client, err := genai.NewClient(ctx, option.WithAPIKey(config.APIKey))
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Return the plan function
	return func(ctx context.Context, session *Session) (*Task, error) {
		// Create a context with timeout
		ctx, cancel := context.WithTimeout(ctx, config.Timeout)
		defer cancel()

		// Get healthy agents
		healthyAgents := registry.ListHealthy()
		if len(healthyAgents) == 0 {
			return nil, fmt.Errorf("no healthy agents available")
		}

		// Convert agents to Gemini function declarations
		tools, err := agentsToTools(registry, healthyAgents)
		if err != nil {
			return nil, fmt.Errorf("failed to convert agents to tools: %w", err)
		}

		// Add a special "no_action" tool to indicate completion
		tools = append(tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{
				{
					Name:        noActionAgentID,
					Description: "Call this when no further action is needed and the task is complete",
					Parameters: &genai.Schema{
						Type: genai.TypeObject,
						Properties: map[string]*genai.Schema{
							"reason": {
								Type:        genai.TypeString,
								Description: "Reason why no action is needed",
							},
						},
						Required: []string{"reason"},
					},
				},
			},
		})

		// Configure the model
		model := client.GenerativeModel(config.Model)
		model.SetTemperature(config.Temperature)
		model.SetMaxOutputTokens(config.MaxTokens)
		model.SystemInstruction = &genai.Content{
			Parts: []genai.Part{genai.Text(config.SystemPrompt)},
		}
		model.Tools = tools

		// Convert session to conversation history
		history := sessionToHistory(session, config.ContextWindow)

		// Start chat session
		chat := model.StartChat()
		chat.History = history

		// Request Gemini to select an agent
		prompt := "Based on the conversation above, which agent should handle the next step? Call the appropriate agent function."
		resp, err := chat.SendMessage(ctx, genai.Text(prompt))
		if err != nil {
			return nil, fmt.Errorf("Gemini request failed: %w", err)
		}

		// Parse function calls from response
		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			return nil, fmt.Errorf("no response from Gemini")
		}

		// Look for function calls in the response
		for _, part := range resp.Candidates[0].Content.Parts {
			if fc, ok := part.(genai.FunctionCall); ok {
				// Check if it's the no_action function
				if fc.Name == noActionAgentID {
					return nil, nil // No more tasks
				}

				// Extract agent ID from function name (format: agent_<id>)
				agentID := fc.Name

				// Get the input from function call args
				inputText := ""
				if input, ok := fc.Args["input"].(string); ok {
					inputText = input
				}

				// Create input content for the agent
				input := []*proto.Content{
					{
						Role:     "user",
						Type:     "text",
						Mimetype: "text/plain",
						Data:     inputText,
					},
				}

				return &Task{
					AgentID:   agentID,
					Inputs:    input,
					Goal:      &Goal{Description: "Process user request using model selected agent"},
					StepIndex: session.CurrentStep(),
				}, nil
			}
		}

		// No function call found, treat as completion
		return nil, nil
	}, nil
}

// agentsToTools converts registry agents to Gemini function declarations.
func agentsToTools(registry *Registry, agentIDs []string) ([]*genai.Tool, error) {
	var tools []*genai.Tool

	for _, id := range agentIDs {
		info, err := registry.GetInfo(id)
		if err != nil {
			continue // Skip agents we can't get info for
		}

		// Create a function declaration for this agent
		funcDecl := &genai.FunctionDeclaration{
			Name:        id, // Use agent ID as function name
			Description: fmt.Sprintf("%s - %s", info.Name, info.Description),
			Parameters: &genai.Schema{
				Type: genai.TypeObject,
				Properties: map[string]*genai.Schema{
					"input": {
						Type:        genai.TypeString,
						Description: "Input text to send to the agent",
					},
				},
				Required: []string{"input"},
			},
		}

		// Add metadata as additional context in the description if available
		if len(info.Metadata) > 0 {
			metadataJSON, _ := json.Marshal(info.Metadata)
			funcDecl.Description += fmt.Sprintf(" (Metadata: %s)", string(metadataJSON))
		}

		tools = append(tools, &genai.Tool{
			FunctionDeclarations: []*genai.FunctionDeclaration{funcDecl},
		})
	}

	return tools, nil
}

// sessionToHistory converts session message history to Gemini conversation format.
func sessionToHistory(session *Session, contextWindow int) []*genai.Content {
	var history []*genai.Content

	// Get recent messages within context window
	startIdx := max(0, len(session.MessageHistory)-contextWindow)
	messages := session.MessageHistory[startIdx:]

	// Convert each message to Gemini format
	for _, msg := range messages {
		role := msg.Role
		// Map roles to Gemini's expected roles
		if role == "assistant" {
			role = "model"
		} else if role != "user" {
			role = "user" // Default to user for other roles
		}

		history = append(history, &genai.Content{
			Role: role,
			Parts: []genai.Part{
				genai.Text(msg.Data),
			},
		})
	}

	return history
}
