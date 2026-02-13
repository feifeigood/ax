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
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	triggerSessionID     string
	triggerInput         string
	triggerCheckpoint    string
	triggerDestSessionID string
	triggerServerAddr    string
	triggerHeadless      bool
	triggerConfigFile    string
)

var triggerCmd = &cobra.Command{
	Use:   "trigger",
	Short: "Trigger a new session or resume an existing one",
	Long: `Trigger a new agentic session or resume an existing one.
If no session ID is provided, a new UUID will be generated.
Use --checkpoint to resume from a specific checkpoint.`,
	RunE: runTrigger,
}

func init() {
	triggerCmd.Flags().StringVar(&triggerSessionID, "session", "", "Session ID (optional, generates UUID if not provided)")
	triggerCmd.Flags().StringVar(&triggerInput, "input", "", "Input message to send (required)")
	triggerCmd.Flags().StringVar(&triggerCheckpoint, "checkpoint", "", "Resume from specific checkpoint UUID (empty for latest)")
	triggerCmd.Flags().StringVar(&triggerDestSessionID, "destination_session", "", "Destination Session ID when forking from checkpoint (optional)")
	triggerCmd.Flags().StringVar(&triggerServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")
	triggerCmd.Flags().BoolVar(&triggerHeadless, "headless", false, "Run in headless mode with a built-in Controller")
	triggerCmd.Flags().StringVar(&triggerConfigFile, "config", "gar.yaml", "Path to YAML configuration file (only used in headless mode)")
	triggerCmd.MarkFlagRequired("input")
}

// TODO(jbd): Add multimodal input flags, e.g. --input-image.

func runTrigger(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Generate UUID if no session ID provided
	if triggerSessionID == "" {
		triggerSessionID = uuid.New().String()
		fmt.Printf("Generated session ID: %s\n", triggerSessionID)
	}

	// Create input content
	inputs := []*proto.Content{
		{
			Role: "user",
			Content: &proto.Content_Text{
				Text: &proto.TextContent{
					Text: triggerInput,
				},
			},
		},
	}

	// Setup signal handling for graceful shutdown
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		fmt.Println("\nReceived interrupt, shutting down...")
		cancel()
	}()

	if triggerHeadless {
		return runHeadless(ctx, triggerSessionID, inputs)
	}

	conn, err := connect(triggerServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)

	// If checkpoint is provided, we fork the session first
	if triggerCheckpoint != "" {
		if triggerDestSessionID == "" {
			triggerDestSessionID = uuid.New().String()
			fmt.Printf("Generated new session ID: %s\n", triggerDestSessionID)
		}
		
		forkedID, err := ForkSession(ctx, client, triggerSessionID, triggerCheckpoint, triggerDestSessionID)
		if err != nil {
			return err
		}
		triggerSessionID = forkedID
	}

	stream, err := client.TriggerSession(ctx, &proto.TriggerSessionRequest{
		SessionId:    triggerSessionID,
		Inputs:       inputs,
	})
	if err != nil {
		return fmt.Errorf("error triggering session: %w", err)
	}

	// Receive and print all responses
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("error receiving response: %w", err)
		}

		if resp.CheckpointId != "" {
			fmt.Printf("Checkpoint ID: %s\n", resp.CheckpointId)
		}

		if resp.Outputs != nil {
			printResponse(resp.State, resp.Outputs)
		}
	}
	return nil
}

func runHeadless(ctx context.Context, sessionID string, inputs []*proto.Content) error {
	// Load configuration from YAML file
	cfg, err := config.LoadFromFile(triggerConfigFile)
	if err != nil {
		return fmt.Errorf("error loading config file '%s': %w", triggerConfigFile, err)
	}

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("invalid configuration: %w", err)
	}

	c, err := newControllerFromConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("error creating controller: %w", err)
	}
	defer c.Close()

	// TODO(lhuan): Allow a default local agent to be registered in headless mode.

	// Create output handler to print streaming results to stdout
	outputHandler := agent.OutputHandler(func(resp *proto.ProcessResponse) error {
		printResponse(proto.State_STATE_RUNNING, resp.Contents)
		return nil
	})

	req := &proto.ProcessRequest{
		CheckpointId: triggerCheckpoint,
		Contents:     inputs,
	}

	if err := c.TriggerSession(ctx, sessionID, req, outputHandler); err != nil {
		return fmt.Errorf("error triggering session in headless mode: %w", err)
	}

	fmt.Println("[COMPLETED]")
	return nil
}

func printResponse(state proto.State, outputs []*proto.Content) {
	for _, content := range outputs {
		switch c := content.Content.(type) {
		case *proto.Content_Text:
			fmt.Printf("[%s] %s\n", state, c.Text.Text)
		default:
			fmt.Printf("[%s] Unknown content type: %T\n", state, c)
		}
	}
}
