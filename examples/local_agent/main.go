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
	"strings"
	"time"

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
		"local-echo-agent",
		"Echo Agent",
		"Simple echo agent that uppercases input",
		map[string]string{
			"version": "1.0",
		}); err != nil {
		log.Fatalf("Error registering agent: %v\n", err)
	}

	inputs := []*proto.Content{
		{
			Role:     "user",
			Type:     "text",
			Mimetype: "text/plain",
			Data:     "Hello, uppercase my message!",
		},
	}

	// Trigger a session
	sessionID := uuid.New().String()
	log.Printf("Session ID: %s\n", sessionID)

	handler := agent.OutputHandler(func(content *proto.Content) error {
		fmt.Printf("Output received: %s\n", content.Data)
		return nil
	})
	if err := c.TriggerSession(ctx, sessionID, inputs, handler); err != nil {
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
				Data:     strings.ToUpper(content.Data),
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

	healthCheckFunc := func(ctx context.Context) error {
		// Always healthy
		return nil
	}

	return agent.NewLocalAgent(agent.LocalAgentConfig{
		ID:              id,
		ProcessFunc:     processFunc,
		HealthCheckFunc: healthCheckFunc,
	})
}
