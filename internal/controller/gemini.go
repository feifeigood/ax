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
	"runtime"
	"time"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
	"google.golang.org/genai"
)

// GeminiConfig configures the Gemini agent.
type GeminiConfig struct {
	APIKey       string        // Google AI API key (for programmatic use only; if empty, uses GEMINI_API_KEY env var - recommended)
	Model        string        // Model name (default: gemini-3-flash-preview)
	MaxTokens    int32         // Max output tokens (default: 8192)
	Timeout      time.Duration // Request timeout (default: 60s)
	SystemPrompt string        // Custom system prompt (optional)
}

// GeminiAgent implements task.Agent using Gemini.
type GeminiAgent struct {
	config GeminiConfig
	client *genai.Client
}

// NewGeminiAgent creates a new Gemini agent.
func NewGeminiAgent(ctx context.Context, config GeminiConfig) (*GeminiAgent, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}
	if config.Model == "" {
		config.Model = "gemini-3-flash-preview"
	}
	if config.APIKey == "" {
		config.APIKey = os.Getenv("GEMINI_API_KEY")
		if config.APIKey == "" {
			return nil, fmt.Errorf("GEMINI_API_KEY not set and no API key provided in config")
		}
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: config.APIKey,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Gemini client: %w", err)
	}
	return &GeminiAgent{
		config: config,
		client: client,
	}, nil
}

func (a *GeminiAgent) Process(ctx context.Context, t *agent.Task, e agent.TaskExecutor, handler agent.OutputHandler) error {
	inputs := t.Inputs
	contents := protoToContents(inputs)
	ctx, cancel := context.WithTimeout(ctx, a.config.Timeout)
	defer cancel()

	var systemPrompt *genai.Content
	if a.config.SystemPrompt != "" {
		systemPrompt = genai.Text(a.config.SystemPrompt)[0]
	}
	resp, err := a.client.Models.GenerateContent(ctx, a.config.Model, contents, &genai.GenerateContentConfig{
		SystemInstruction: systemPrompt,
		MaxOutputTokens:   a.config.MaxTokens,
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
			if err := handler(&proto.ProcessResponse{
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

const bashToolName = "bash"

func newBashTool() *bashTool {
	return &bashTool{}
}

// bashTool is the built-in tool that allows
// planner to execute general purpose bash commands.
type bashTool struct{}

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
