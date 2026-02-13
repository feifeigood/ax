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
	"time"

	"github.com/google/gar/proto"
	"github.com/spf13/cobra"
)

var (
	inspectSessionID  string
	inspectServerAddr string
)

var inspectCmd = &cobra.Command{
	Use:   "inspect",
	Short: "Inspect a session",
	Long:  `Inspect a session to view its current state, step count, and other details.`,
	RunE:  runInspect,
}

func init() {
	inspectCmd.Flags().StringVar(&inspectSessionID, "session", "", "Session ID (required)")
	inspectCmd.Flags().StringVar(&inspectServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")
	inspectCmd.MarkFlagRequired("session")
}

func runInspect(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	conn, err := connect(inspectServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)

	// Get session details
	resp, err := client.GetSession(ctx, &proto.GetSessionRequest{
		SessionId: inspectSessionID,
	})
	if err != nil {
		return fmt.Errorf("error getting session: %w", err)
	}

	session := resp.Session

	// Print session details
	fmt.Println("\nSession Details:")
	fmt.Printf("  ID: %s\n", inspectSessionID)
	fmt.Printf("  State: %s\n", session.State)
	fmt.Printf("  Created At: %s\n", session.CreatedAt.AsTime().Format(time.RFC3339))
	fmt.Printf("  Updated At: %s\n", session.UpdatedAt.AsTime().Format(time.RFC3339))
	fmt.Printf("  Checkpoints: %v\n", session.CheckpointIds)
	fmt.Printf("  Active Agents: %v\n", session.ActiveAgents)

	return nil
}
