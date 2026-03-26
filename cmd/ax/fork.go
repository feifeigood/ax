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

	"github.com/google/ax/proto"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var (
	forkSourceID     string
	forkCheckpointID string
	forkDestID       string
	forkServerAddr   string
)

var forkCmd = &cobra.Command{
	Use:   "fork",
	Short: "Fork an event log from a specific checkpoint",
	Long: `Fork an existing agentic event log from a specific checkpoint.
If --dest-id is not provided, a new UUID will be generated.`,
	RunE: runFork,
}

func init() {
	forkCmd.Flags().StringVar(&forkSourceID, "src-id", "", "Source ID to fork from (required)")
	forkCmd.Flags().StringVar(&forkCheckpointID, "src-checkpoint", "", "Checkpoint ID to fork from (optional, defaults to latest)")
	forkCmd.Flags().StringVar(&forkDestID, "dest-id", "", "Destination ID (optional, generates UUID if not provided)")
	forkCmd.Flags().StringVar(&forkServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")

	forkCmd.MarkFlagRequired("src-id")
}

func runFork(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()

	// Generate UUID if no destination ID provided
	if forkDestID == "" {
		forkDestID = uuid.New().String()
		fmt.Printf("Generated destination ID: %s\n", forkDestID)
	}

	conn, err := connect(forkServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewAXServiceClient(conn)

	_, err = Fork(ctx, client, forkSourceID, forkCheckpointID, forkDestID)
	return err
}

// Fork forks a execution from a checkpoint and returns the new execution ID.
func Fork(ctx context.Context, client proto.AXServiceClient, sourceID, checkpointID, destID string) (string, error) {
	resp, err := client.Fork(ctx, &proto.ForkRequest{
		SrcId:           sourceID,
		SrcCheckpointId: checkpointID,
		DestId:          destID,
	})
	if err != nil {
		return "", fmt.Errorf("error forking: %w", err)
	}
	return resp.NewId, nil
}
