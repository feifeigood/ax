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
		// Send failure response
		stream.Send(&proto.TriggerSessionResponse{
			SessionId: sessionID,
			State:     proto.State_STATE_FAILED,
		})
		return err
	}

	// Get session state
	session, err := s.controller.GetSession(sessionID)
	if err != nil {
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

// RegisterAgent registers a new agent with the dispatcher.
func (s *Server) RegisterAgent(ctx context.Context, req *proto.RegisterAgentRequest) (*proto.RegisterAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	registry := s.controller.Registry()

	// Register based on agent type
	var err error
	switch req.AgentType {
	case "remote":
		if req.Address == "" {
			return nil, fmt.Errorf("address is required for remote agents")
		}
		err = registry.RegisterRemote(req.AgentId, req.Name, req.Description, req.Address, req.Metadata)
	case "local":
		return nil, fmt.Errorf("local agents must be registered programmatically")
	default:
		return nil, fmt.Errorf("unknown agent type: %s", req.AgentType)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to register agent: %w", err)
	}

	return &proto.RegisterAgentResponse{}, nil
}

// UnregisterAgent removes an agent from the dispatcher.
func (s *Server) UnregisterAgent(ctx context.Context, req *proto.UnregisterAgentRequest) (*proto.UnregisterAgentResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if req.AgentId == "" {
		return nil, fmt.Errorf("agent_id is required")
	}

	registry := s.controller.Registry()
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
