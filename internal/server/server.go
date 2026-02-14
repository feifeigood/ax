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

// Package server implements the gRPC server for GARService,
// exposing session management and agent registration APIs.

package server

import (
	"context"
	"fmt"
	"net"

	"google.golang.org/grpc"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/internal/controller"
	"github.com/google/gar/proto"
)

// Server implements the GARService gRPC service.
type Server struct {
	proto.UnimplementedGARServiceServer

	controller *controller.Controller
}

// New creates a new controller server.
func New(c *controller.Controller) *Server {
	return &Server{
		controller: c,
	}
}

// TriggerSession triggers a new agentic loop session with streaming responses.
func (s *Server) TriggerSession(req *proto.TriggerSessionRequest, stream grpc.ServerStreamingServer[proto.TriggerSessionResponse]) error {
	sessionID := req.SessionId
	inputs := req.Inputs

	// TODO(jbd): This state update should be sent directly from the controller.
	if err := stream.Send(&proto.TriggerSessionResponse{
		State:     proto.State_STATE_RUNNING,
		SessionId: sessionID,
	}); err != nil {
		return err
	}

	incoming := &proto.ProcessRequest{
		Contents: inputs,
	}
	// Create output handler to stream outputs back to client
	outputHandler := agent.OutputHandler(func(outgoing *proto.ProcessResponse) error {
		return stream.Send(&proto.TriggerSessionResponse{
			SessionId:    sessionID,
			CheckpointId: outgoing.CheckpointId,
			Outputs:      outgoing.Contents,
			State:        proto.State_STATE_RUNNING,
		})
	})
	return s.controller.TriggerSession(
		stream.Context(), sessionID, incoming, outputHandler)
}

func (s *Server) ForkSession(ctx context.Context, req *proto.ForkSessionRequest) (*proto.ForkSessionResponse, error) {
	if err := s.controller.ForkSession(ctx, req.SrcSessionId, req.SrcCheckpointId, req.DestSessionId); err != nil {
		return nil, err
	}

	return &proto.ForkSessionResponse{
		NewSessionId: req.DestSessionId,
	}, nil
}

// RegisterAgent registers a new remote agent with the controller.
func (s *Server) RegisterAgent(ctx context.Context, req *proto.RegisterAgentRequest) (*proto.RegisterAgentResponse, error) {
	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	if req.Address == "" {
		return nil, fmt.Errorf("address is required for remote agents")
	}

	registry := s.controller.Registry()

	// All registered agents are remote
	err := registry.RegisterRemote(config.RemoteAgentConfig{
		ID:          req.AgentId,
		Name:        req.Name,
		Description: req.Description,
		Address:     req.Address,
		Metadata:    req.Metadata,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to register agent: %w", err)
	}

	return &proto.RegisterAgentResponse{}, nil
}

// UnregisterAgent removes a remote agent from the controller.
// Local agents cannot be unregistered via this API.
func (s *Server) UnregisterAgent(ctx context.Context, req *proto.UnregisterAgentRequest) (*proto.UnregisterAgentResponse, error) {
	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	registry := s.controller.Registry()

	// Check if the agent is local
	info, err := registry.GetInfo(req.AgentId)
	if err != nil {
		return nil, fmt.Errorf("agent not found: %w", err)
	}

	if info.Type == controller.AgentTypeLocal {
		return nil, fmt.Errorf("cannot unregister local agents via API")
	}

	if err := registry.Unregister(req.AgentId); err != nil {
		return nil, fmt.Errorf("failed to unregister agent: %w", err)
	}

	return &proto.UnregisterAgentResponse{}, nil
}

// Serve starts the gRPC server on the specified address.
func (s *Server) Serve(address string, opts ...grpc.ServerOption) error {
	lis, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	grpcServer := grpc.NewServer(opts...)
	proto.RegisterGARServiceServer(grpcServer, s)

	if err := grpcServer.Serve(lis); err != nil {
		return fmt.Errorf("failed to serve: %w", err)
	}

	return nil
}
