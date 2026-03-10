package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/internal/controller"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
)

func main() {
	ctx := context.Background()
	input := "Send the word 'oRanGe' to the local-echo-agent. Take its exact output and send it to the remote-text-processor. Take its exact output and send it to the uppercase agent. Return the final output."
	sessionID := uuid.New().String()

	// 1. Create a local agent
	echoAgent, err := createLocalAgent()
	if err != nil {
		log.Fatalf("Error creating local agent: %v\n", err)
	}

	// 2. Initialize controller
	c, err := controller.New(ctx, controller.Config{
		MaxSteps: 15,
		HealthCheck: config.HealthCheckConfig{
			Enabled:  false, // disable to speed up
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
	if err := c.Registry().RegisterRemote(config.RemoteAgentConfig{
		ID:          "remote-text-processor",
		Name:        "Remote Text Processor",
		Description: "Adds the prefix 'Remote Prefix: ' to the text",
		Address:     "localhost:50051",
	}); err != nil {
		log.Fatalf("Error registering remote agent: %v\n", err)
	}

	// 5. Register Sandbox Agent
	if err := c.Registry().RegisterSandbox(ctx, config.SandboxAgentConfig{
		ID:                 "uppercase",
		SandboxTemplateRef: "uppercase-agent-template",
		ContainerPort:      8494,
		UseRouter:          true,
	}); err != nil {
		log.Fatalf("Error registering sandbox agent: %v\n", err)
	}

	inputs := []*proto.Content{
		{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: input,
				},
			},
		},
	}

	log.Printf("Session ID: %s\n", sessionID)

	handler := agent.OutputHandler(func(outgoing *proto.ProcessResponse) error {
		for _, c := range outgoing.Contents {
			if textContent := c.GetText(); textContent != nil {
				fmt.Printf("Output received: %s\n", textContent.Text)
			}
		}
		return nil
	})
	
	req := &proto.ProcessRequest{
		Contents: inputs,
	}

	for i := 0; i < 4; i++ {
		log.Printf("\n--- Triggering step %d ---\n", i+1)
		if err := c.TriggerSession(ctx, sessionID, req, handler); err != nil {
			log.Fatalf("Error triggering session step %d: %v\n", i+1, err)
		}
		// Subsequent triggers just ask the planner to continue processing the existing history
		req = &proto.ProcessRequest{}
	}
}

func createLocalAgent() (*agent.LocalAgent, error) {
	processFunc := func(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
		for _, content := range incoming.Contents {
			textContent := content.GetText()
			if textContent == nil {
				continue
			}
			if err := handler(&proto.ProcessResponse{
				Contents: []*proto.Content{
					{
						Role: "assistant",
						Content: &proto.Content_Text{
							Text: &proto.TextContent{
								Text: strings.ToLower(textContent.Text),
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

func (m *mockPlanner) ID() string   { return "__planner" }
func (m *mockPlanner) Name() string { return "Mock Planner" }
func (m *mockPlanner) HealthCheck(ctx context.Context) error { return nil }
func (m *mockPlanner) Close() error { return nil }
func (m *mockPlanner) Process(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	var lastText string
	for _, c := range incoming.Contents {
		if t := c.GetText(); t != nil {
			lastText = t.Text
		}
	}

	// Step 1: User -> Local
	if strings.HasPrefix(lastText, "Send the word") {
		return handler(&proto.ProcessResponse{
			AgentHandoff: "local-echo-agent",
			Contents: []*proto.Content{{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{Text: "oRanGe"},
				},
			}},
		})
	}

	// Step 2: Local -> Remote
	if lastText == "orange" {
		return handler(&proto.ProcessResponse{
			AgentHandoff: "remote-text-processor",
			Contents: []*proto.Content{{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{Text: lastText},
				},
			}},
		})
	}

	// Step 3: Remote -> Sandbox
	if strings.HasPrefix(lastText, "Remote Prefix:") {
		return handler(&proto.ProcessResponse{
			AgentHandoff: "uppercase",
			Contents: []*proto.Content{{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{Text: lastText},
				},
			}},
		})
	}

	// Final step: Sandbox -> Done
	if strings.Contains(lastText, "UPPERCASE") {
		return handler(&proto.ProcessResponse{
			Contents: []*proto.Content{{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Final Result: " + lastText,
					},
				},
			}},
		})
	}

	return nil
}
