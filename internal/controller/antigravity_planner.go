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
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.See
// the License for the specific language governing permissions and limitations
// under the License.

package controller

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/proto"
)

const (
	streamResponseInitialBufferSize = 64 * 1024   // 64 KB
	streamResponseMaxBufferSize     = 1024 * 1024 // 1 MB
)

// AntigravityPlannerConfig configures the Antigravity-based planner.
type AntigravityPlannerConfig struct {
	Endpoint string
	Timeout  string
}

// AntigravityPlannerAgent implements the agent.Agent interface by calling an external Python server.
type AntigravityPlannerAgent struct {
	registry   *Registry
	config     AntigravityPlannerConfig
	httpClient *http.Client
}

// NewAntigravityPlannerAgent creates a new Antigravity-based planner agent.
func NewAntigravityPlannerAgent(ctx context.Context, registry *Registry, cfg AntigravityPlannerConfig) (agent.Agent, error) {
	timeoutStr := cfg.Timeout
	if timeoutStr == "" {
		// TODO(lhuan): optimize and reduce latency
		timeoutStr = "5m" // Default timeout
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse duration %q: %w", timeoutStr, err)
	}
	return &AntigravityPlannerAgent{
		registry:   registry,
		config:     cfg,
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// Connect starts the agent loop.
func (p *AntigravityPlannerAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	return p.loop(ctx, execID, start, e, handler)
}

// loop is the main execution loop for the Antigravity planner.
func (p *AntigravityPlannerAgent) loop(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	payload, err := p.preparePayload(execID, start)
	if err != nil {
		return err
	}

	resp, err := p.sendRequest(ctx, payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return p.handleStreamingResponse(ctx, resp, handler)
}

func (p *AntigravityPlannerAgent) preparePayload(execID string, start *proto.AgentStart) ([]byte, error) {
	type Message struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	var messages []Message
	for _, msg := range start.Messages {
		role := msg.Role
		text := ""
		if msg.Content != nil {
			switch content := msg.Content.Type.(type) {
			case *proto.Content_Text:
				text = content.Text.Text
			}
			// TODO: Add support for other content types.
		}
		messages = append(messages, Message{
			Role: role,
			Text: text,
		})
	}

	reqData := map[string]interface{}{
		"conversation_id": execID,
		"messages":        messages,
	}

	return json.Marshal(reqData)
}

func (p *AntigravityPlannerAgent) sendRequest(ctx context.Context, payload []byte) (*http.Response, error) {
	log.Printf("Sending request to Antigravity Server at %s", p.config.Endpoint)
	req, err := http.NewRequestWithContext(ctx, "POST", p.config.Endpoint, bytes.NewBuffer(payload))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to call Antigravity Server: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("Antigravity Server returned error status: %s", resp.Status)
	}

	return resp, nil
}

func (p *AntigravityPlannerAgent) handleStreamingResponse(ctx context.Context, resp *http.Response, handler agent.OutputHandler) error {
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, streamResponseInitialBufferSize)
	scanner.Buffer(buf, streamResponseMaxBufferSize)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// TODO(lhuan): to update / add more types per antigravity proto
		var result struct {
			Type     string `json:"type"`
			Text     string `json:"text,omitempty"`
			ToolCall *struct {
				Name string                 `json:"name"`
				Args map[string]interface{} `json:"args"`
			} `json:"tool_call,omitempty"`
		}
		if err := json.Unmarshal(line, &result); err != nil {
			log.Printf("Failed to unmarshal streaming chunk: %v", err)
			continue
		}

		switch result.Type {
		case "thinking":
			handler(&proto.AgentOutputs{
				Messages: []*proto.Message{
					{
						Role: "assistant",
						Content: &proto.Content{
							Type: &proto.Content_Thought{
								Thought: &proto.ThoughtContent{
									Summary: []*proto.ThoughtSummaryContent{
										{
											Type: &proto.ThoughtSummaryContent_Text{
												Text: &proto.TextContent{Text: result.Text},
											},
										},
									},
								},
							},
						},
					},
				},
			})

		case "text":
			handler(&proto.AgentOutputs{
				Messages: []*proto.Message{
					{
						Role: "assistant",
						Content: &proto.Content{
							Type: &proto.Content_Text{
								Text: &proto.TextContent{Text: result.Text},
							},
						},
					},
				},
			})

		// TODO(lhuan): Support tool_call and tool_result types when needed.
		default:
			log.Printf("Warning: unhandled response type: %s", result.Type)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading streaming response: %w", err)
	}

	return nil
}

// HealthCheck implements the agent.Agent interface.
func (p *AntigravityPlannerAgent) HealthCheck(ctx context.Context) error {
	return nil
}

// Close implements the agent.Agent interface.
func (p *AntigravityPlannerAgent) Close() error {
	return nil
}
