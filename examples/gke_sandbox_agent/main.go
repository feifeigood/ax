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
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/google/gar/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type server struct {
	proto.UnimplementedAgentServiceServer
}

func (s *server) HealthCheck(ctx context.Context, req *proto.HealthCheckRequest) (*proto.HealthCheckResponse, error) {
	return &proto.HealthCheckResponse{Healthy: true, Message: "upper_case_agent is running"}, nil
}

func (s *server) Process(stream grpc.BidiStreamingServer[proto.ProcessRequest, proto.ProcessResponse]) error {
	for {
		// Read input from the orchestrator
		req, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		var outputs []*proto.Content
		
		if len(req.Contents) > 0 {
			// Find the most recent message from the "user" in the session history
			var targetText string
			for i := len(req.Contents) - 1; i >= 0; i-- {
				if text := req.Contents[i].GetText(); text != nil && req.Contents[i].Role == "user" {
					targetText = text.Text
					break
				}
			}

			if targetText != "" {
				// Convert to upper case dynamically!
				upper := strings.ToUpper(targetText)

				log.Printf("📥 Processed resolved text: %q", targetText)
				log.Printf("📤 Sending response: %q", upper)

				outputs = append(outputs, &proto.Content{
					Role: "agent",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "Hey, I'm your sandbox agent.\n",
						},
					},
				})
				outputs = append(outputs, &proto.Content{
					Role: "agent",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: fmt.Sprintf("here is your upper case text: %s", upper),
						},
					},
				})
			}
		}

		if len(outputs) > 0 {
			// Send response back via gRPC
			if err := stream.Send(&proto.ProcessResponse{
				Contents: outputs,
			}); err != nil {
				return err
			}
		}
	}
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "50051" // Default port for local testing
	}

	// 1. Listen on port
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", port, err)
	}

	// 2. Create gRPC server
	s := grpc.NewServer()
	proto.RegisterAgentServiceServer(s, &server{})
	reflection.Register(s)

	// 3. Graceful shutdown handler
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Println("Shutting down upper_case_agent...")
		s.GracefulStop()
	}()

	log.Printf("🟢 upper_case_agent listening on :%s", port)
	if err := s.Serve(lis); err != nil {
		log.Fatalf("failed to serve: %v", err)
	}
}
