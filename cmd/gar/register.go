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
	"github.com/spf13/cobra"
)

var (
	registerAgentID    string
	registerAgentName  string
	registerAgentDesc  string
	registerAgentAddr  string
	registerServerAddr string
)

var registerCmd = &cobra.Command{
	Use:   "register",
	Short: "Register a remote agent",
	Long:  `Register a remote agent with the controller so it can be used in sessions.`,
	RunE:  runRegister,
}

func init() {
	registerCmd.Flags().StringVar(&registerAgentID, "agent-id", "", "Agent ID (required)")
	registerCmd.Flags().StringVar(&registerAgentName, "agent-name", "", "Agent name (required)")
	registerCmd.Flags().StringVar(&registerAgentDesc, "agent-description", "", "Agent description (required)")
	registerCmd.Flags().StringVar(&registerAgentAddr, "agent-addr", "", "Agent address (e.g., localhost:50051) (required)")
	registerCmd.Flags().StringVar(&registerServerAddr, "server", "localhost:8494", "gRPC controller server address (default: localhost:8494)")
	registerCmd.MarkFlagRequired("agent-id")
	registerCmd.MarkFlagRequired("agent-name")
	registerCmd.MarkFlagRequired("agent-description")
	registerCmd.MarkFlagRequired("agent-addr")
}

func runRegister(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	conn, err := connect(registerServerAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	client := proto.NewGARServiceClient(conn)

	// Register remote agent
	_, err = client.RegisterAgent(ctx, &proto.RegisterAgentRequest{
		AgentId:     registerAgentID,
		Name:        registerAgentName,
		Description: registerAgentDesc,
		Config: &proto.RegisterAgentRequest_Remote{
			Remote: &proto.RemoteAgentConfig{
				Address: registerAgentAddr,
			},
		},
	})
	if err != nil {
		return fmt.Errorf("error registering agent: %w", err)
	}
	return nil
}
