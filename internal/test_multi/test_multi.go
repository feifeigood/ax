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

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller"
	"github.com/google/ax/internal/controller/executor"
	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

func main() {
	ctx := context.Background()
	input := "Send the word 'oRanGe' to the local-echo-agent. Take its exact output and send it to the remote-text-processor. Take its exact output and send it to the uppercase agent. Return the final output."
	execID := uuid.New().String()

	// 1. Create a local agent
	echoAgent, err := createLocalAgent()
	if err != nil {
		log.Fatalf("Error creating local agent: %v\n", err)
	}

	// 2. Initialize controller
	c, err := controller.New(ctx, controller.Config{
		HealthCheck: config.HealthCheckConfig{
			Enabled: false, // disable to speed up
		},
		EventLogBuilder: func() (executor.EventLog, error) {
			return executor.OpenSQLiteEventLog(filepath.Join(os.TempDir(), "test_multi.db"))
		},
		PlannerBuilder: func(ctx context.Context, r *controller.Registry) (agent.Agent, error) {
			return &mockPlanner{}, nil
		},
	})
	if err != nil {
		log.Fatalf("Error creating controller: %v\n", err)
	}
	defer c.Close()

	// 3. Register Local Agent
	if err := c.Registry().RegisterLocal(config.LocalAgentConfig{
		ID:          "local-echo-agent",
		Name:        "Local Echo Agent",
		Description: "Converts text to lowercase",
		Agent:       echoAgent,
	}); err != nil {
		log.Fatalf("Error registering local agent: %v\n", err)
	}

	// 4. Register Remote Agent
	if _, err := c.Registry().RegisterRemote(config.RemoteAgentConfig{
		ID:          "remote-text-processor",
		Name:        "Remote Text Processor",
		Description: "Adds the prefix 'Remote Prefix: ' to the text",
		Address:     "localhost:50051",
	}); err != nil {
		log.Fatalf("Error registering remote agent: %v\n", err)
	}

	// 5. Register Sandbox Agent
	if err := c.Registry().RegisterKubernetesSandbox(ctx, config.SandboxAgentConfig{
		ID:                 "uppercase",
		SandboxTemplateRef: "uppercase-agent-template",
		ContainerPort:      8494,
		UseRouter:          true,
	}); err != nil {
		log.Fatalf("Error registering sandbox agent: %v\n", err)
	}

	inputs := []*proto.Message{
		{
			Role: "user",
			Content: &proto.Content{
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: input,
					},
				},
			},
		},
	}

	log.Printf("ID: %s\n", execID)

	handler := agent.OutputHandler(func(outgoing *proto.AgentOutputs) error {
		for _, m := range outgoing.Messages {
			if textContent := m.GetContent().GetText(); textContent != nil {
				fmt.Printf("Output received: %s\n", textContent.Text)
			}
		}
		return nil
	})

	for i := range 4 {
		log.Printf("\n--- Executing step %d ---\n", i+1)
		if err := c.Exec(ctx, &proto.ExecRequest{
			ConversationId: execID,
			AgentId:        "planner",
			Inputs:         inputs,
		}, handler); err != nil {
			log.Fatalf("Error executing step %d: %v\n", i+1, err)
		}
	}
}

func createLocalAgent() (*agent.LocalAgent, error) {
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
							Content: &proto.Content_Text{
								Text: &proto.TextContent{
									Text: strings.ToLower(textContent.Text),
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
				Content: &proto.Content_Text{
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
				Content: &proto.Content_Text{
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
				Content: &proto.Content_Text{
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
					Content: &proto.Content_Text{
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
