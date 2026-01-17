package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/gar/proto"
)

const port = ":50051"

// server implements the AgentService gRPC server.
type server struct {
	proto.UnimplementedAgentServiceServer
	agentID string
}

func newServer(agentID string) *server {
	return &server{
		agentID: agentID,
	}
}

// Process implements bidirectional streaming for content processing.
// This RPC is called by the gar dispatcher when a session triggers this agent.
func (s *server) Process(stream proto.AgentService_ProcessServer) error {
	log.Println("Process stream started - gar dispatcher connected")

	for {
		// Receive input content from gar dispatcher
		content, err := stream.Recv()
		if err == io.EOF {
			log.Println("Process stream closed by gar dispatcher")
			return nil
		}
		if err != nil {
			log.Printf("Error receiving from dispatcher: %v", err)
			return err
		}

		log.Printf("Received from dispatcher: role=%s, type=%s, data=%s",
			content.Role, content.Type, content.Data)

		// Process the content (simple uppercase echo)
		response := &proto.Content{
			Role:     "assistant",
			Type:     content.Type,
			Mimetype: content.Mimetype,
			Data:     fmt.Sprintf("Remote Echo: %s", strings.ToUpper(content.Data)),
		}

		// Send response back to gar dispatcher
		if err := stream.Send(response); err != nil {
			log.Printf("Error sending to dispatcher: %v", err)
			return err
		}

		log.Printf("Sent to dispatcher: %s", response.Data)
	}
}

// StreamLifecycle streams lifecycle events from the agent.
func (s *server) StreamLifecycle(stream proto.AgentService_StreamLifecycleServer) error {
	log.Println("Lifecycle stream started")

	// Send initial PROGRESS event
	if err := stream.Send(&proto.LifecycleEvent{
		EventType: "PROGRESS",
		Timestamp: timestamppb.Now(),
		Metadata: map[string]string{
			"status": "started",
		},
	}); err != nil {
		return err
	}

	// Send periodic heartbeats
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			event := &proto.LifecycleEvent{
				EventType: "PROGRESS",
				Timestamp: timestamppb.Now(),
				Metadata: map[string]string{
					"status": "healthy",
				},
			}

			if err := stream.Send(event); err != nil {
				log.Printf("Error sending lifecycle event: %v", err)
				return err
			}

			log.Println("Sent lifecycle event")

		case <-stream.Context().Done():
			log.Println("Lifecycle stream closed")
			return nil
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
	const agentID = "remote-echo-agent"

	fmt.Printf("Listening on port: %s\n", port)
	lis, err := net.Listen("tcp", port)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	proto.RegisterAgentServiceServer(grpcServer, newServer(agentID))

	fmt.Println("\nAgent server is running...")
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
