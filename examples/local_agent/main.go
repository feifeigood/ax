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
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/internal/controller"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
)

var (
	sessionID string
	input     string
)

func main() {
	ctx := context.Background()
	flag.StringVar(&input, "input", "Hello, uppercase this message!", "Input message to be processed by the agent")
	flag.StringVar(&sessionID, "session_id", "", "Optional session ID")
	flag.Parse()

	// Create a local echo agent
	echoAgent, err := createEchoAgent()
	if err != nil {
		log.Fatalf("Error creating agent: %v\n", err)
	}

	c, err := controller.New(ctx, controller.Config{
		MaxSteps: 10,
		HealthCheck: config.HealthCheckConfig{
			Enabled:  true,
			Interval: 30 * time.Second,
		},
	})
	if err != nil {
		log.Fatalf("Error creating controller: %v\n", err)
	}
	defer c.Close()

	if err := c.Registry().RegisterLocal(config.LocalAgentConfig{
		ID:          "local-echo-agent",
		Name:        "Echo Agent",
		Description: "Simple echo agent that uppercases input",
		Metadata: map[string]string{
			"version": "1.0",
		},
		Agent: echoAgent,
	}); err != nil {
		log.Fatalf("Error registering agent: %v\n", err)
	}

	inputs := []*proto.Content{
		{
			Role:     "user",
			Type:     "text",
			Mimetype: "text/plain",
			Data:     input,
		},
	}

	// Trigger a session. Alternatively, controller can be used
	// with the server package to expose a gRPC server.
	if sessionID == "" {
		sessionID = uuid.New().String()
	}
	log.Printf("Session ID: %s\n", sessionID)

	handler := agent.OutputHandler(func(outgoing *proto.ProcessResponse) error {
		for _, c := range outgoing.Contents {
			fmt.Printf("Output received: %s\n", c.Data)
		}
		return nil
	})
	if err := c.TriggerSession(ctx, sessionID, &proto.ProcessRequest{
		Contents: inputs,
	}, handler); err != nil {
		log.Fatalf("Error triggering session: %v\n", err)
	}
}

// createEchoAgent creates a simple echo agent that repeats input with a prefix.
func createEchoAgent() (*agent.LocalAgent, error) {
	processFunc := func(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
		// Process each input and call handler with response
		for _, content := range incoming.Contents {
			if err := handler(&proto.ProcessResponse{
				Contents: []*proto.Content{
					{
						Role:     "assistant",
						Type:     content.Type,
						Mimetype: content.Mimetype,
						Data:     strings.ToUpper(content.Data),
					},
				},
			}); err != nil {
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
		ProcessFunc:     processFunc,
		HealthCheckFunc: healthCheckFunc,
	})
}
