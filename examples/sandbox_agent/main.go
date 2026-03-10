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
		for _, content := range req.Contents {
			// Only process text messages
			if textContent := content.GetText(); textContent != nil {
				// Convert to upper case dynamically!
				upper := strings.ToUpper(textContent.Text)

				log.Printf("📥 Received request text: %q", textContent.Text)
				log.Printf("📤 Sending response: %q", upper)

				outputs = append(outputs, &proto.Content{
					Role: "agent",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: "hey im your sandbox agent",
						},
					},
				})
				outputs = append(outputs, &proto.Content{
					Role: "agent",
					Content: &proto.Content_Text{
						Text: &proto.TextContent{
							Text: fmt.Sprintf("HERE IS YOUR UPPERCASE TEXT: %s", upper),
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
