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

// Package controller implements the single-writer orchestrator that coordinates
// agentic loops, manages executions, and communicates with local and remote agents.
package controller

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/config"
	"github.com/google/ax/internal/controller/executor"
	"github.com/google/ax/internal/testagent"
	"github.com/google/ax/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const plannerAgentID = "__planner"

// Controller is the main controller that coordinates all components.
// It acts as a single-writer system for managing agentic loops.
type Controller struct {
	inFlightExecutionsMu sync.Mutex
	inFlightExecutions   map[string]struct{}
	registry             *Registry
	eventLog             executor.EventLog
	plannerBuilder       PlannerBuilder
}

// PlannerBuilder is a function that creates a PlanFunc given a Registry.
type PlannerBuilder func(ctx context.Context, r *Registry) (agent.Agent, error)

// Config configures the controller.
type Config struct {
	EventLogBuilder executor.EventLogBuilder
	PlannerBuilder  PlannerBuilder
	// TODO(jbd): Add CompacterBuilder.
	HealthCheck config.HealthCheckConfig
}

// New creates a new controller instance.
func New(ctx context.Context, config Config) (*Controller, error) {
	// Initialize agent registry
	registry, err := NewRegistry(config.HealthCheck)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry: %w", err)
	}

	// Determine plan function
	// If no planner builder is provided, use the default Gemini planner.
	if config.PlannerBuilder == nil {
		config.PlannerBuilder = func(ctx context.Context, r *Registry) (agent.Agent, error) {
			return NewGeminiPlannerAgent(ctx, r, GeminiPlannerConfig{})
		}
	}

	if config.EventLogBuilder == nil {
		return nil, fmt.Errorf("event log builder is required")
	}
	eventLog, err := config.EventLogBuilder()
	if err != nil {
		return nil, fmt.Errorf("failed to create event log: %w", err)
	}

	return &Controller{
		inFlightExecutions: make(map[string]struct{}),
		registry:           registry,
		eventLog:           eventLog,
		plannerBuilder:     config.PlannerBuilder,
	}, nil
}

// Exec executes a new agentic loop execution or resumes an existing one.
// If id is empty, a UUID will be generated.
// If the execution already exists, it will be resumed with optional new inputs.
func (d *Controller) Exec(ctx context.Context, incoming *proto.AgentMessage, handler agent.OutputHandler) error {
	if incoming.ExecId == "" {
		return fmt.Errorf("id is required")
	}

	inFlight, cleanup := d.markInFlight(incoming.ExecId)
	defer cleanup()

	if inFlight {
		return fmt.Errorf("execution %q is already in flight", incoming.ExecId)
	}

	planner, err := d.plannerBuilder(ctx, d.registry)
	if err != nil {
		return fmt.Errorf("failed to create planner: %w", err)
	}
	registry := d.registry.Map()
	registry[plannerAgentID] = planner
	registry["gemini"] = NewGeminiAgent()

	// For testing only! Remove this once the project is stable.
	// TODO(jbd): Remove this before the release.
	if os.Getenv("AX_TEST_AGENTS") == "1" {
		for id, agent := range testagent.Agents() {
			registry[id] = agent
		}
	}

	start := incoming.GetStart()
	if start == nil {
		return fmt.Errorf("no start message")
	}
	if start.AgentId == "" {
		start.AgentId = plannerAgentID
	}
	e := executor.DefaultExecutor(d.eventLog, registry)
	return e.Exec(ctx, incoming.ExecId, start, handler)
}

// Fork forks an execution from a source execution.
// If checkpointId is provided, fork til the checkpoint. Otherwise, fork the whole execution.
func (d *Controller) Fork(ctx context.Context, sourceID, sourceCheckpoint, destID string) error {
	if sourceID == "" {
		return fmt.Errorf("source ID is required")
	}
	if destID == "" {
		return fmt.Errorf("destination ID is required")
	}
	return status.Errorf(codes.Unimplemented, "forking is not supported yet")
}

// Registry returns the agent registry.
func (d *Controller) Registry() *Registry {
	return d.registry
}

// Close gracefully shuts down the controller.
func (d *Controller) Close() error {
	if err := d.eventLog.Close(); err != nil {
		return fmt.Errorf("failed to close event log: %w", err)
	}
	if err := d.registry.Close(); err != nil {
		return fmt.Errorf("failed to close registry: %w", err)
	}
	return nil
}

func (d *Controller) markInFlight(id string) (exists bool, cleanup func()) {
	d.inFlightExecutionsMu.Lock()
	defer d.inFlightExecutionsMu.Unlock()

	_, ok := d.inFlightExecutions[id]
	if ok {
		return true, func() {}
	}
	d.inFlightExecutions[id] = struct{}{}

	return false, func() {
		d.inFlightExecutionsMu.Lock()
		delete(d.inFlightExecutions, id)
		d.inFlightExecutionsMu.Unlock()
	}
}
