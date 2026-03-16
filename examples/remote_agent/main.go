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
	"log"
	"net"

	"google.golang.org/grpc"

	"github.com/google/ax/proto"
	"github.com/google/uuid"
)

const port = ":50051"

// server implements the AgentService gRPC server.
type server struct {
	proto.UnimplementedAgentServiceServer
}

// Process implements bidirectional streaming for content processing.
// This RPC is called by the gar controller when a session triggers this agent.
func (s *server) Process(stream proto.AgentService_ProcessServer) error {
	for {
		// Receive input content from gar controller
		incoming, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		var contents []*proto.Content
		for _, input := range incoming.Contents {
			textContent := input.GetText()
			if textContent == nil {
				continue
			}
			contents = append(contents, &proto.Content{
				Role: "assistant",
				Content: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Remote Prefix: " + textContent.Text,
					},
				},
			})
		}
		if err := stream.Send(&proto.ProcessResponse{
			Contents:     contents,
			CheckpointId: uuid.New().String(),
		}); err != nil {
			return err
		}
	}
}

// HealthCheck checks if the agent is healthy.
func (s *server) HealthCheck(ctx context.Context, req *proto.HealthCheckRequest) (*proto.HealthCheckResponse, error) {
	log.Println("Health check requested")

	return &proto.HealthCheckResponse{
		Healthy: true,
		Message: "Agent is healthy",
	}, nil
}

func main() {
	fmt.Printf("Listening on port: %s\n", port)
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	proto.RegisterAgentServiceServer(grpcServer, &server{})

	fmt.Println("\nAgent server is running...")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
