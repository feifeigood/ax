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

package internal_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller"
	"github.com/google/ax/internal/controller/executor"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

func TestMulti(t *testing.T) {
	ctx := context.Background()
	input := "Send the word 'oRanGe' to the local-echo-agent. Take its exact output and send it to the remote-text-processor. Take its exact output and send it to the uppercase agent. Return the final output."
	execID := uuid.New().String()

	// 1. Create local agents with specific behaviors to make the test self-contained.
	echoAgent, err := createLocalAgent(func(s string) string { return strings.ToLower(s) })
	if err != nil {
		t.Fatalf("Error creating local agent: %v", err)
	}

	remoteAgent, err := createLocalAgent(func(s string) string { return "Remote Prefix: " + s })
	if err != nil {
		t.Fatalf("Error creating remote agent: %v", err)
	}

	uppercaseAgent, err := createLocalAgent(func(s string) string { return "UPPERCASE: " + strings.ToUpper(s) })
	if err != nil {
		t.Fatalf("Error creating uppercase agent: %v", err)
	}

	// 2. Initialize controller
	dbPath := filepath.Join(t.TempDir(), "test_multi.db")
	c, err := controller.New(ctx, controller.Config{
		EventLogBuilder: func() (executor.EventLog, error) {
			return executor.OpenSQLiteEventLog(dbPath)
		},
		PlannerBuilder: func(ctx context.Context, r *controller.Registry) (agent.Agent, error) {
			return &mockPlanner{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Error creating controller: %v", err)
	}
	defer c.Close()

	// 3. Register Agents
	if err := c.Registry().RegisterLocal(config.LocalAgentConfig{
		ID:          "local-echo-agent",
		Name:        "Local Echo Agent",
		Description: "Converts text to lowercase",
		Agent:       echoAgent,
	}); err != nil {
		t.Fatalf("Error registering local agent: %v", err)
	}

	if err := c.Registry().RegisterLocal(config.LocalAgentConfig{
		ID:          "remote-text-processor",
		Name:        "Remote Text Processor",
		Description: "Adds the prefix 'Remote Prefix: ' to the text",
		Agent:       remoteAgent,
	}); err != nil {
		t.Fatalf("Error registering remote agent: %v", err)
	}

	if err := c.Registry().RegisterLocal(config.LocalAgentConfig{
		ID:          "uppercase",
		Name:        "Uppercase Agent",
		Description: "Converts text to uppercase",
		Agent:       uppercaseAgent,
	}); err != nil {
		t.Fatalf("Error registering sandbox agent: %v", err)
	}

	inputs := []*proto.Message{
		{
			Role: "user",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: input,
					},
				},
			},
		},
	}

	t.Logf("ID: %s", execID)

	var finalResult string
	handler := controller.ExecHandler(func(resp *proto.ExecResponse) error {
		for _, m := range resp.Outputs {
			if textContent := m.GetContent().GetText(); textContent != nil {
				t.Logf("Output received: %s", textContent.Text)
				if strings.HasPrefix(textContent.Text, "Final Result:") {
					finalResult = textContent.Text
				}
			}
		}
		return nil
	})

	for i := range 4 {
		t.Logf("\n--- Executing step %d ---", i+1)
		var reqInputs []*proto.Message
		if i == 0 {
			reqInputs = inputs
		}
		if err := c.Exec(ctx, &proto.ExecRequest{
			ConversationId: execID,
			AgentId:        "__planner",
			Inputs:         reqInputs,
		}, handler); err != nil {
			t.Fatalf("Error executing step %d: %v", i+1, err)
		}
	}

	if finalResult == "" {
		t.Fatal("Expected a final result, but got none")
	}
	expected := "Final Result: UPPERCASE: REMOTE PREFIX: ORANGE"
	if finalResult != expected {
		t.Fatalf("Expected final result %q, got %q", expected, finalResult)
	}
}

func createLocalAgent(transform func(string) string) (*agent.LocalAgent, error) {
	processFunc := func(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
		for _, msg := range start.Messages {
			content := msg.GetContent()
			if content == nil {
				continue
			}
			textContent := content.GetText()
			if textContent == nil {
				continue
			}
			if err := handler(&proto.AgentOutputs{
				Messages: []*proto.Message{
					{
						Role: "assistant",
						Content: &proto.Content{
							Type: &proto.Content_Text{
								Text: &proto.TextContent{
									Text: transform(textContent.Text),
								},
							},
						},
					},
				},
			}); err != nil {
				return err
			}
		}
		return nil
	}

	return agent.NewLocalAgent(agent.LocalAgentConfig{
		ProcessFunc:     processFunc,
		HealthCheckFunc: func(ctx context.Context) error { return nil },
	})
}

type mockPlanner struct{}

func (m *mockPlanner) ID() string                            { return "__planner" }
func (m *mockPlanner) Name() string                          { return "Mock Planner" }
func (m *mockPlanner) HealthCheck(ctx context.Context) error { return nil }
func (m *mockPlanner) Close() error                          { return nil }
func (m *mockPlanner) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, handler agent.OutputHandler) error {
	var lastText string
	for _, m := range start.Messages {
		if textMsg := m.GetContent().GetText(); textMsg != nil {
			lastText = textMsg.Text
		}
	}

	// Step 1: User -> Local
	if strings.HasPrefix(lastText, "Send the word") {
		inputs := append(start.Messages, &proto.Message{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: "oRanGe"},
				},
			},
		})
		_, err := e.Exec(ctx, "local-echo", &proto.AgentStart{
			AgentId:  "local-echo-agent",
			Messages: inputs,
		}, handler)
		return err
	}

	// Step 2: Local -> Remote
	if lastText == "orange" {
		inputs := append(start.Messages, &proto.Message{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: lastText},
				},
			},
		})
		_, err := e.Exec(ctx, "remote-text", &proto.AgentStart{
			AgentId:  "remote-text-processor",
			Messages: inputs,
		}, handler)
		return err
	}

	// Step 3: Remote -> Sandbox
	if strings.HasPrefix(lastText, "Remote Prefix:") {
		inputs := append(start.Messages, &proto.Message{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{Text: lastText},
				},
			},
		})
		_, err := e.Exec(ctx, "uppercase-task", &proto.AgentStart{
			AgentId:  "uppercase",
			Messages: inputs,
		}, handler)
		return err
	}

	// Final step: Sandbox -> Done
	if strings.Contains(lastText, "UPPERCASE") {
		return handler(&proto.AgentOutputs{
			Messages: []*proto.Message{{
				Role: "assistant",
				Content: &proto.Content{
					Type: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "Final Result: " + lastText,
						},
					},
				},
			}},
		})
	}

	return nil
}
