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

package controller

// TODO(lhuan): Setup a better automated testing framework

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/proto"
)

// MockAgent is a mock implementation of the Agent interface for testing.
type MockAgent struct {
	ProcessFunc     func(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error
	healthCheckFunc func(ctx context.Context) error
	closeFunc       func() error
}

func (m *MockAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	if m.ProcessFunc != nil {
		return m.ProcessFunc(ctx, execID, start, e, o)
	}
	return nil
}

func (m *MockAgent) HealthCheck(ctx context.Context) error {
	if m.healthCheckFunc != nil {
		return m.healthCheckFunc(ctx)
	}
	return nil
}

func (m *MockAgent) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}

type mockAgentServer struct {
	proto.UnimplementedAgentServiceServer
	healthy bool
}

func (s *mockAgentServer) HealthCheck(ctx context.Context, req *proto.HealthCheckRequest) (*proto.HealthCheckResponse, error) {
	return &proto.HealthCheckResponse{Healthy: s.healthy}, nil
}

func startMockGRPCServer(t *testing.T, healthy bool) (string, func()) {
	lis, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	s := grpc.NewServer()
	proto.RegisterAgentServiceServer(s, &mockAgentServer{healthy: healthy})
	go func() {
		if err := s.Serve(lis); err != nil {
			// server might be closed
		}
	}()
	return lis.Addr().String(), func() {
		s.Stop()
		lis.Close()
	}
}

func TestRegistry_HealthCheckScenarios(t *testing.T) {
	tests := []struct {
		name          string
		enabled       bool
		agentType     string
		expectHealthy bool
	}{
		{
			name:          "Local Agent (Always Healthy)",
			enabled:       true,
			agentType:     "local",
			expectHealthy: true,
		},
		{
			name:          "Local Agent - Check Disabled (Always Healthy)",
			enabled:       false,
			agentType:     "local",
			expectHealthy: true,
		},
		{
			name:          "Remote Agent - Check Enabled (Eventually Healthy)",
			enabled:       true,
			agentType:     "remote",
			expectHealthy: true, // Eventually true
		},
		{
			name:          "Remote Agent - Check Disabled (Optimistically Healthy)",
			enabled:       false,
			agentType:     "remote",
			expectHealthy: true, // Optimistically true
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Mock server for remote agent
			address, cleanup := startMockGRPCServer(t, true)
			defer cleanup()

			cfg := config.HealthCheckConfig{
				Enabled:  tt.enabled,
				Interval: 10 * time.Millisecond,
			}
			r, err := NewRegistry(cfg)
			if err != nil {
				t.Fatalf("NewRegistry failed: %v", err)
			}
			defer r.Close()

			id := "test-agent"

			if tt.agentType == "local" {
				// Mock local agent
				validMockAgent := &MockAgent{}
				err = r.RegisterLocal(config.LocalAgentConfig{
					ID:    id,
					Name:  id,
					Agent: validMockAgent,
				})
				if err != nil {
					t.Fatalf("RegisterLocal failed: %v", err)
				}
			} else {
				err = r.RegisterRemote(config.RemoteAgentConfig{
					ID:      id,
					Name:    id,
					Address: address,
				})
				if err != nil {
					t.Fatalf("RegisterRemote failed: %v", err)
				}
			}

			// Verify health
			if tt.agentType == "remote" && tt.enabled {
				// Must wait for health check
				timeout := time.After(2 * time.Second)
				ticker := time.NewTicker(20 * time.Millisecond)
				defer ticker.Stop()

				success := false
				for !success {
					select {
					case <-timeout:
						t.Fatal("timed out waiting for agent to become healthy")
					case <-ticker.C:
						info, err := r.GetInfo(id)
						if err != nil {
							t.Fatalf("GetInfo failed: %v", err)
						}
						// Remote agents might start unhealthy until checked
						if info.Healthy {
							success = true
						}
					}
				}
			} else {
				// Should be immediately healthy (local or disabled)
				// Important: for disabled+remote, it is optimistically healthy.
				// For local, it is always healthy (unless manually updated, but default is true).

				// Wait a tiny bit just in case async things happen, but usually sync for registration
				time.Sleep(10 * time.Millisecond)

				info, err := r.GetInfo(id)
				if err != nil {
					t.Fatalf("GetInfo failed: %v", err)
				}
				if info.Healthy != tt.expectHealthy {
					t.Errorf("expected healthy=%v, got %v", tt.expectHealthy, info.Healthy)
				}
			}
		})
	}
}

func TestRegistry_GracefulShutdown(t *testing.T) {
	healthCheckConfig := config.HealthCheckConfig{
		Enabled:  true,
		Interval: 10 * time.Millisecond,
	}
	r, regErr := NewRegistry(healthCheckConfig)
	if regErr != nil {
		t.Fatalf("NewRegistry failed: %v", regErr)
	}

	// Register multiple agents to create workload
	for i := range 50 {
		_ = r.RegisterRemote(config.RemoteAgentConfig{
			ID:      "remote-shutdown-test-" + string(rune(i)),
			Name:    "Shutdown Test Remote",
			Address: "localhost:1234",
		})
	}

	// Let it run for a bit to ensure performChecks runs.
	time.Sleep(50 * time.Millisecond)

	// Close should return specific errors for failed agents, but NOT panic or deadlock
	// We are testing for absence of panic/deadlock here.
	_ = r.Close()
}
