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

package agent

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/google/gar/proto"
)

// RemoteAgent is a gRPC client that implements the Agent interface.
// It communicates with remote agent services over gRPC.
type RemoteAgent struct {
	address    string
	conn       *grpc.ClientConn
	client     proto.AgentServiceClient
	mu         sync.Mutex
	reconnect  bool
	maxRetries int
	dialOpts   []grpc.DialOption
}

// RemoteAgentConfig configures a remote agent client.
type RemoteAgentConfig struct {
	Address    string            // gRPC server address (e.g., "localhost:50051")
	Reconnect  bool              // Whether to automatically reconnect on failures
	MaxRetries int               // Maximum number of retry attempts (0 = infinite)
	DialOpts   []grpc.DialOption // gRPC dial options for customizing the connection
}

// NewRemoteAgent creates a new remote agent client.
func NewRemoteAgent(config RemoteAgentConfig) (*RemoteAgent, error) {
	if config.Address == "" {
		return nil, fmt.Errorf("agent address cannot be empty")
	}

	agent := &RemoteAgent{
		address:    config.Address,
		reconnect:  config.Reconnect,
		maxRetries: config.MaxRetries,
		dialOpts:   config.DialOpts,
	}

	if err := agent.connect(); err != nil {
		return nil, fmt.Errorf("failed to connect to remote agent: %w", err)
	}

	return agent, nil
}

// connect establishes a gRPC connection to the remote agent.
func (a *RemoteAgent) connect() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn != nil {
		a.conn.Close()
	}

	// Use provided dial options, or default to insecure credentials
	opts := a.dialOpts
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}

	conn, err := grpc.NewClient(a.address, opts...)
	if err != nil {
		return fmt.Errorf("failed to dial: %w", err)
	}

	a.conn = conn
	a.client = proto.NewAgentServiceClient(conn)
	return nil
}

// Process handles processing of input content with the remote agent.
func (a *RemoteAgent) Process(ctx context.Context, t *Task, e TaskExecutor, o OutputHandler) error {
	ctx = metadata.AppendToOutgoingContext(ctx, "execution-id", t.ID)

	stream, err := a.client.Process(ctx)
	if err != nil {
		return fmt.Errorf("failed to create stream: %w", err)
	}

	// Send all inputs to the remote agent
	if err := stream.Send(&proto.ProcessRequest{Contents: t.Inputs}); err != nil {
		return fmt.Errorf("failed to send content: %w", err)
	}

	// Close the send direction to signal we're done sending
	if err := stream.CloseSend(); err != nil {
		return fmt.Errorf("failed to close send: %w", err)
	}

	// Receive outputs and call handler for each
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			// Stream completed successfully
			return nil
		}
		if err != nil {
			return fmt.Errorf("failed to receive content: %w", err)
		}

		// Call the handler with the received content
		if err := o(resp); err != nil {
			return fmt.Errorf("handler error: %w", err)
		}

		// Check for context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
	}
}

// HealthCheck checks if the remote agent is healthy.
func (a *RemoteAgent) HealthCheck(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	resp, err := a.client.HealthCheck(ctx, &proto.HealthCheckRequest{})
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}

	if !resp.Healthy {
		return fmt.Errorf("agent unhealthy: %s", resp.Message)
	}

	return nil
}

// Close gracefully shuts down the remote agent connection.
func (a *RemoteAgent) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.conn != nil {
		return a.conn.Close()
	}
	return nil
}
