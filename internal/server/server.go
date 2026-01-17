// Package server implements the gRPC server for GARService,
// exposing session management and agent registration APIs.
package server

import (
	"context"
	"fmt"
	"net"
	"sync"

	"google.golang.org/grpc"

	"github.com/google/gar/internal/controller"
	"github.com/google/gar/proto"
)

// Server implements the GARService gRPC service.
type Server struct {
	proto.UnimplementedGARServiceServer

	mu         sync.RWMutex
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
	s.mu.Lock()
	sessionID := req.SessionId
	inputs := req.Inputs
	checkpointID := req.CheckpointId
	s.mu.Unlock()

	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	if err := stream.Send(&proto.TriggerSessionResponse{
		SessionId: sessionID,
		State:     proto.State_STATE_STARTING,
	}); err != nil {
		return err
	}

	// Trigger the session
	if err := s.controller.TriggerSession(stream.Context(), sessionID, inputs, checkpointID); err != nil {
		stream.Send(&proto.TriggerSessionResponse{
			SessionId: sessionID,
			State:     proto.State_STATE_FAILED,
		})
		return err
	}

	// Get session state
	session, err := s.controller.GetSession(sessionID)
	if err != nil {
		stream.Send(&proto.TriggerSessionResponse{
			SessionId: sessionID,
			State:     proto.State_STATE_FAILED,
		})
		return err
	}

	// Get the latest checkpoint ID if available
	latestCheckpointID := ""
	if len(session.CheckpointIDs) > 0 {
		latestCheckpointID = session.CheckpointIDs[len(session.CheckpointIDs)-1]
	}

	// Send final success response
	return stream.Send(&proto.TriggerSessionResponse{
		SessionId:    sessionID,
		State:        session.State,
		CheckpointId: latestCheckpointID,
	})
}

// GetSession retrieves session details.
func (s *Server) GetSession(ctx context.Context, req *proto.GetSessionRequest) (*proto.GetSessionResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if req.SessionId == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	// Load session if not already loaded
	session, err := s.controller.GetSession(req.SessionId)
	if err != nil {
		// Try loading from event log
		session, err = s.controller.LoadSession(req.SessionId)
		if err != nil {
			return nil, fmt.Errorf("session not found: %w", err)
		}
	}

	return &proto.GetSessionResponse{
		Session: &proto.SessionInfo{
			SessionId:       session.ID,
			State:           session.State,
			CurrentStep:     int32(session.CurrentStep),
			ActiveAgents:    session.ActiveAgents,
			CreatedAt:       session.CreatedAt.UnixMilli(),
			UpdatedAt:       session.UpdatedAt.UnixMilli(),
			MessageCount:    int32(len(session.MessageHistory)),
			CheckpointCount: int32(len(session.CheckpointIDs)),
		},
	}, nil
}

// ListSessions lists all available sessions.
func (s *Server) ListSessions(ctx context.Context, req *proto.ListSessionsRequest) (*proto.ListSessionsResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	sessions, err := s.controller.ListSessions()
	if err != nil {
		return nil, fmt.Errorf("failed to list sessions: %w", err)
	}

	return &proto.ListSessionsResponse{
		SessionIds: sessions,
	}, nil
}

// RegisterAgent registers a new remote agent with the dispatcher.
func (s *Server) RegisterAgent(ctx context.Context, req *proto.RegisterAgentRequest) (*proto.RegisterAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	if req.Address == "" {
		return nil, fmt.Errorf("address is required for remote agents")
	}

	registry := s.controller.Registry()

	// All registered agents are remote
	err := registry.RegisterRemote(req.AgentId, req.Name, req.Description, req.Address, req.Metadata)
	if err != nil {
		return nil, fmt.Errorf("failed to register agent: %w", err)
	}

	return &proto.RegisterAgentResponse{}, nil
}

// UnregisterAgent removes a remote agent from the dispatcher.
// Local agents cannot be unregistered via this API.
func (s *Server) UnregisterAgent(ctx context.Context, req *proto.UnregisterAgentRequest) (*proto.UnregisterAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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
