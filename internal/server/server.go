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
	"google.golang.org/grpc/status"

	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller"
	"github.com/google/ax/proto"
)

// Server implements the AXService gRPC service.
type Server struct {
	proto.UnimplementedAXServiceServer

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

func (s *Server) Fork(ctx context.Context, req *proto.ForkRequest) (*proto.ForkResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "forking not implemented")
}

// RegisterAgent registers a new remote agent with the controller.
func (s *Server) RegisterAgent(ctx context.Context, req *proto.RegisterAgentRequest) (*proto.RegisterAgentResponse, error) {
	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("name is required")
	}
	if req.Description == "" {
		return nil, fmt.Errorf("description is required")
	}
	if req.Config == nil {
		return nil, fmt.Errorf("config is required")
	}

	registry := s.controller.Registry()

	var healthy bool
	switch cfg := req.Config.(type) {
	case *proto.RegisterAgentRequest_Remote:
		if cfg.Remote.Address == "" {
			return nil, fmt.Errorf("address is required for remote agents")
		}

		var err error
		healthy, err = registry.RegisterRemote(config.RemoteAgentConfig{
			ID:          req.AgentId,
			Name:        req.Name,
			Description: req.Description,
			Address:     cfg.Remote.Address,
			Metadata:    req.Metadata,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to register agent: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown agent type")
	}

	return &proto.RegisterAgentResponse{
		Healthy: healthy,
	}, nil
}

// Serve starts the gRPC server on the specified address.
func (s *Server) Serve(address string, opts ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	s.grpcServer = grpc.NewServer(opts...)
	proto.RegisterAXServiceServer(s.grpcServer, s)

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
