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

	"github.com/google/gar/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	forkSourceSessionID string
	forkCheckpointID    string
	forkDestSessionID   string
	forkInput           string
	forkServerAddr      string
)

var forkCmd = &cobra.Command{
	Use:   "fork",
	Short: "Fork a session from a specific checkpoint",
	Long: `Fork an existing agentic session from a specific checkpoint.
If --destination_session is not provided, a new UUID will be generated.`,
	RunE: runFork,
}

func init() {
	forkCmd.Flags().StringVar(&forkSourceSessionID, "source_session", "", "Source Session ID to fork from (required)")
	forkCmd.Flags().StringVar(&forkCheckpointID, "checkpoint", "", "Checkpoint ID to fork from (optional, defaults to latest)")
	forkCmd.Flags().StringVar(&forkDestSessionID, "destination_session", "", "Destination Session ID (optional, generates UUID if not provided)")
	forkCmd.Flags().StringVar(&forkServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")

	forkCmd.MarkFlagRequired("source_session")
}

func runFork(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	// Generate UUID if no destination session ID provided
	if forkDestSessionID == "" {
		forkDestSessionID = uuid.New().String()
		fmt.Printf("Generated destination session ID: %s\n", forkDestSessionID)
	}

	conn, err := connect(forkServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)

	_, err = ForkSession(ctx, client, forkSourceSessionID, forkCheckpointID, forkDestSessionID)
	return err
}

// ForkSession forks a session from a checkpoint and returns the new session ID.
func ForkSession(ctx context.Context, client proto.GARServiceClient, sourceSessionID, checkpointID, destSessionID string) (string, error) {
	fmt.Printf("Forking from source session %s at checkpoint %s to destination session %s\n", sourceSessionID, checkpointID, destSessionID)

	resp, err := client.ForkSession(ctx, &proto.ForkSessionRequest{
		SourceSessionId:    sourceSessionID,
		SourceCheckpointId: checkpointID,
		DestSessionId:      destSessionID,
	})
	if err != nil {
		return "", fmt.Errorf("error forking session: %w", err)
	}

	fmt.Printf("Forked session ID: %s\n", resp.NewSessionId)
	return resp.NewSessionId, nil
}
