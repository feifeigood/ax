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
	"fmt"
	"io"
	"log"
	"net"

	"google.golang.org/grpc"

	"github.com/google/ax/proto"
)

const port = ":50051"

// server implements the AgentService gRPC server.
type server struct {
	proto.UnimplementedAgentServiceServer
}

// Connect implements bidirectional streaming for agent processing.
func (s *server) Connect(stream grpc.BidiStreamingServer[proto.AgentMessage, proto.AgentMessage]) error {
	for {
		incoming, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		start := incoming.GetStart()
		if start == nil {
			continue
		}

		msg := &proto.Message{
			Role: "assistant",
			Content: &proto.Content{
				Type: &proto.Content_Text{
					Text: &proto.TextContent{
						Text: "Hello World",
					},
				},
			},
		}

		if err := stream.Send(&proto.AgentMessage{
			ConversationId: incoming.ConversationId,
			ExecId:         incoming.ExecId,
			Type: &proto.AgentMessage_Outputs{
				Outputs: &proto.AgentOutputs{
					Messages: []*proto.Message{msg},
				},
			},
		}); err != nil {
			return err
		}

		// Send AgentEnd to signal end of outputs.
		if err := stream.Send(&proto.AgentMessage{
			ConversationId: incoming.ConversationId,
			ExecId:         incoming.ExecId,
			Type: &proto.AgentMessage_End{
				End: &proto.AgentEnd{},
			},
		}); err != nil {
			return err
		}
	}
}

func main() {
	fmt.Printf("Listening on port: %s\n", port)
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	proto.RegisterAgentServiceServer(grpcServer, &server{})

	fmt.Println("\nATE Agent server is starting...")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
