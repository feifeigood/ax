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

	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	triggerSessionID  string
	triggerInput      string
	triggerCheckpoint string
	triggerServerAddr string
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
	// TODO(jbd): Enable a -headless option that can run
	// the controller without a gar server.

	triggerCmd.Flags().StringVar(&triggerSessionID, "session", "", "Session ID (optional, generates UUID if not provided)")
	triggerCmd.Flags().StringVar(&triggerInput, "input", "", "Input message to send (required)")
	triggerCmd.Flags().StringVar(&triggerCheckpoint, "checkpoint", "", "Resume from specific checkpoint UUID (empty for latest)")
	triggerCmd.Flags().StringVar(&triggerServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")
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
			Role:     "user",
			Type:     "text",
			Mimetype: "text/plain",
			Data:     triggerInput,
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

	conn, err := connect(triggerServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)
	stream, err := client.TriggerSession(ctx, &proto.TriggerSessionRequest{
		SessionId:    triggerSessionID,
		Inputs:       inputs,
		CheckpointId: triggerCheckpoint,
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

		if resp.Outputs != nil {
			for _, output := range resp.Outputs {
				fmt.Printf("[%s] %s\n", resp.State, output.Data)
			}
		}
	}
	return nil
}
