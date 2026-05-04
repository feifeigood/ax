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

// Package server implements the gRPC server for AXService,
// exposing execution management and agent registration APIs.

package server

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"github.com/google/ax/internal/controller"
	"github.com/google/ax/proto"
)

// Server implements the AXService gRPC service.
type Server struct {
	proto.UnimplementedControllerServiceServer
	proto.UnimplementedEventLogServiceServer

	controller *controller.Controller
	grpcServer *grpc.Server
}

// New creates a new controller server.
func New(c *controller.Controller) *Server {
	return &Server{
		controller: c,
	}
}

// Exec executes a new agentic task with streaming responses.
func (s *Server) Exec(req *proto.ExecRequest, stream grpc.ServerStreamingServer[proto.ExecResponse]) error {
	// Create output handler to stream outputs back to client
	outputHandler := controller.ExecHandler(func(resp *proto.ExecResponse) error {
		return stream.Send(resp)
	})
	return s.controller.Exec(stream.Context(), req, outputHandler)
}

func (s *Server) ForkConversation(ctx context.Context, req *proto.ForkRequest) (*proto.ForkResponse, error) {
	if req.SrcConversationId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "src_conversation_id is required")
	}
	destID, err := s.controller.Fork(ctx, req.SrcConversationId, req.SrcSeq, req.DestConversationId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to fork conversation: %v", err)
	}
	return &proto.ForkResponse{ConversationId: destID}, nil
}

func (s *Server) DeleteConversation(ctx context.Context, req *proto.DeleteRequest) (*proto.DeleteResponse, error) {
	if req.ConversationId == "" {
		return nil, status.Errorf(codes.InvalidArgument, "conversation_id is required")
	}
	if err := s.controller.Delete(ctx, req.ConversationId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete conversation: %v", err)
	}
	return &proto.DeleteResponse{}, nil
}

// Serve starts the gRPC server on the specified address.
func (s *Server) Serve(address string, opts ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	s.grpcServer = grpc.NewServer(opts...)
	proto.RegisterControllerServiceServer(s.grpcServer, s)
	proto.RegisterEventLogServiceServer(s.grpcServer, s)

	// Register standard gRPC Health Check server
	hs := health.NewServer()
	hs.SetServingStatus("AX", grpc_health_v1.HealthCheckResponse_SERVING)
	grpc_health_v1.RegisterHealthServer(s.grpcServer, hs)

	if err := s.grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve: %w", err)
	}

	return nil
}

// GracefulStop stops the gRPC server gracefully.
func (s *Server) GracefulStop() {
	if s.controller != nil {
		s.controller.Close()
	}
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}
