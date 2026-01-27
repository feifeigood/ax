package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/controller"
	"github.com/google/gar/internal/eventlog"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
)

func main() {
	ctx := context.Background()

	// Create a local echo agent
	echoAgent, err := createEchoAgent("local-echo-agent")
	if err != nil {
		log.Fatalf("Error creating agent: %v\n", err)
	}

	c, err := controller.New(ctx, controller.Config{
		EventLogFactory: func(sessionID string) (eventlog.EventLog, error) {
			return eventlog.NewFileEventLog(eventlog.FileConfig{
				SessionID: sessionID,
				Dir:       "eventlog",
			})
		},
		MaxSteps:            10,
		HealthCheckInterval: 30 * time.Second,
	})
	if err != nil {
		log.Fatalf("Error creating controller: %v\n", err)
	}
	defer c.Close()

	if err := c.Registry().RegisterLocal(
		echoAgent,
		"Echo Agent",
		"Simple echo agent that uppercases input",
		map[string]string{
			"version": "1.0",
		}); err != nil {
		log.Fatalf("Error registering agent: %v\n", err)
	}

	fmt.Println("Agent registered successfully")
	inputs := []*proto.Content{
		{
			Role:     "user",
			Type:     "text",
			Mimetype: "text/plain",
			Data:     "Hello, echo agent!",
		},
	}

	// Trigger a session
	sessionID := uuid.New().String()
	if err := c.TriggerSession(ctx, sessionID, inputs); err != nil {
		log.Fatalf("Error triggering session: %v\n", err)
	}
}

// createEchoAgent creates a simple echo agent that repeats input with a prefix.
func createEchoAgent(id string) (*agent.LocalAgent, error) {
	processFunc := func(ctx context.Context, sessionID string, inputs []*proto.Content, handler agent.OutputHandler) error {
		// Process each input and call handler with response
		for _, content := range inputs {
			// Echo the content back with a prefix
			response := &proto.Content{
				Role:     "assistant",
				Type:     content.Type,
				Mimetype: content.Mimetype,
				Data:     fmt.Sprintf("Echo (session %s): %s", sessionID, strings.ToUpper(content.Data)),
			}

			// Call handler with the response
			if err := handler(response); err != nil {
				return err
			}

			// Check for context cancellation
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}
		}

		return nil
	}

	lifecycleFunc := func(ctx context.Context, handler agent.LifecycleHandler) error {
		// Send initial PROGRESS event
		if err := handler(&proto.LifecycleEvent{
			EventType: proto.EventType_EVENT_TYPE_PROGRESS,
			Timestamp: timestamppb.Now(),
		}); err != nil {
			return err
		}

		// Send periodic heartbeats
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := handler(&proto.LifecycleEvent{
					EventType: proto.EventType_EVENT_TYPE_PROGRESS,
					Timestamp: timestamppb.Now(),
				}); err != nil {
					return err
				}
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}

	healthCheckFunc := func(ctx context.Context) error {
		// Always healthy
		return nil
	}

	return agent.NewLocalAgent(agent.LocalAgentConfig{
		ID:              id,
		ProcessFunc:     processFunc,
		LifecycleFunc:   lifecycleFunc,
		HealthCheckFunc: healthCheckFunc,
	})
}
