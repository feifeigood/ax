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

	"github.com/google/ax/proto"
	"fmt"
)

// LocalAgent wraps a local (in-process) agent implementation.
// It implements the Agent interface for agents running in the same process as the controller.
type LocalAgent struct {
	processFunc     func(ctx context.Context, execID string, start *proto.AgentStart, e Executor, o OutputHandler) error
	healthCheckFunc func(ctx context.Context) error
}

// LocalAgentConfig configures a local agent.
type LocalAgentConfig struct {
	ProcessFunc     func(ctx context.Context, execID string, start *proto.AgentStart, e Executor, o OutputHandler) error
	HealthCheckFunc func(ctx context.Context) error
}

// NewLocalAgent creates a new local agent with the provided configuration.
func NewLocalAgent(config LocalAgentConfig) (*LocalAgent, error) {
	if config.ProcessFunc == nil {
		return nil, fmt.Errorf("ProcessFunc cannot be nil")
	}

	// Provide defaults for optional functions
	if config.HealthCheckFunc == nil {
		config.HealthCheckFunc = func(ctx context.Context) error { return nil }
	}

	return &LocalAgent{
		processFunc:     config.ProcessFunc,
		healthCheckFunc: config.HealthCheckFunc,
	}, nil
}

// Connect handles processing of input content with callback handler.
func (a *LocalAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e Executor, o OutputHandler) error {
	return a.processFunc(ctx, execID, start, e, o)
}

// HealthCheck checks if the agent is healthy.
func (a *LocalAgent) HealthCheck(ctx context.Context) error {
	return a.healthCheckFunc(ctx)
}

// Close gracefully shuts down the agent.
func (a *LocalAgent) Close() error {
	// Local agents don't typically need cleanup, but this can be extended
	return nil
}
