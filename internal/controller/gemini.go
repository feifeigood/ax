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
	"time"

	"github.com/google/gar/proto"
	"google.golang.org/genai"
)

// GeminiPlannerConfig configures the Gemini-based planner.
type GeminiPlannerConfig struct {
	APIKey       string        // Google AI API key (for programmatic use only; if empty, uses GEMINI_API_KEY env var - recommended)
	Model        string        // Model name (default: gemini-3-flash-preview)
	MaxTokens    int32         // Max output tokens (default: 8192)
	Timeout      time.Duration // Request timeout (default: 60s)
	SystemPrompt string        // Custom system prompt (optional)
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
		config.SystemPrompt = `You are an intelligent agent orchestrator. Your role is to analyze the conversation history and user requests, then select the most appropriate agent to handle the task.

Available agents have been provided to you as function tools. Each agent has:
- A unique ID
- A description of its capabilities

Your job is to:
1. Analyze the current conversation context and understand what needs to be done
2. Select the best agent for the task by calling the appropriate function
3. If enough work is done, stop to indicate completion

Guidelines:
- Choose agents based on their capabilities and the user's needs
- If no suitable agent exists, stop.
- Keep the conversation context in mind when selecting agents`
	}

	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: config.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}

	// Return the plan function
	return func(ctx context.Context, inputs []*proto.Content) ([]*Task, error) {
		// Create a context with timeout
		ctx, cancel := context.WithTimeout(ctx, config.Timeout)
		defer cancel()

		// Convert agents to Gemini function declarations
		tools, err := agentsToTools(registry)
		if err != nil {
			return nil, fmt.Errorf("failed to convert agents to tools: %w", err)
		}

		// Convert session to conversation history
		contents := protoToContents(inputs)
		resp, err := client.Models.GenerateContent(ctx, config.Model, contents, &genai.GenerateContentConfig{
			Tools:             tools,
			SystemInstruction: genai.Text(config.SystemPrompt)[0],
			MaxOutputTokens:   config.MaxTokens,
			CandidateCount:    1,
		})

		if err != nil {
			return nil, fmt.Errorf("failed to generate in planner: %w", err)
		}
		if len(resp.Candidates) == 0 {
			return nil, fmt.Errorf("no candidates from Gemini in planner")
		}
		candidate := resp.Candidates[0]
		if candidate.Content == nil || candidate.Content.Parts == nil {
			if candidate.FinishReason == genai.FinishReasonStop {
				return nil, nil // No more tasks
			}
			return nil, fmt.Errorf("no content in candidates from Gemini")
		}

		// Look for function calls in the response
		for _, part := range candidate.Content.Parts {
			if part == nil {
				continue
			}

			if fc := part.FunctionCall; fc != nil {
				return []*Task{{
					AgentID: fc.Name,
					Inputs:  inputs,
				}}, nil
			}
		}
		return nil, nil
	}, nil
}

// agentsToTools converts registry agents to Gemini function declarations.
func agentsToTools(registry *Registry) ([]*genai.Tool, error) {
	healthyAgents := registry.ListHealthy()
	if len(healthyAgents) == 0 {
		return nil, fmt.Errorf("no healthy agents available")
	}

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
	return tools, nil
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
		contents = append(contents, &genai.Content{
			Role: role,
			Parts: []*genai.Part{
				{
					Text: msg.Data,
					// TODO(jbd): Handle other content types (e.g., images, files)
				},
			},
		})
	}

	return contents
}
