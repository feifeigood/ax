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

import (
	"context"
	"fmt"

	"github.com/google/gar/agent"
	"github.com/google/gar/proto"
	"golang.org/x/sync/errgroup"
)

// Task represents a task to be executed by an agent.
type Task struct {
	AgentID string
	Inputs  []*proto.Content
}

// LoopExecutor orchestrates the agentic loop workflow.
// It implements the plan-execute-evaluate-checkpoint cycle.
type LoopExecutor struct {
	registry       *Registry
	sessionManager *SessionManager
	maxSteps       int
	planFunc       PlanFunc
}

// PlanFunc determines the next agent task to execute.
// It receives the current session state and returns the next task.
type PlanFunc func(ctx context.Context, inputs []*proto.Content) ([]*Task, error)

// LoopConfig configures the loop executor.
type LoopConfig struct {
	Registry       *Registry
	SessionManager *SessionManager
	MaxSteps       int
	PlanFunc       PlanFunc
}

// NewLoopExecutor creates a new loop executor.
func NewLoopExecutor(ctx context.Context, config LoopConfig) (*LoopExecutor, error) {
	if config.Registry == nil {
		return nil, fmt.Errorf("registry cannot be nil")
	}
	if config.SessionManager == nil {
		return nil, fmt.Errorf("session manager cannot be nil")
	}
	if config.MaxSteps == 0 {
		config.MaxSteps = 10 // Default max steps per trigger
	}
	// Plan function is required
	if config.PlanFunc == nil {
		return nil, fmt.Errorf("plan function is required")
	}

	return &LoopExecutor{
		registry:       config.Registry,
		sessionManager: config.SessionManager,
		maxSteps:       config.MaxSteps,
		planFunc:       config.PlanFunc,
	}, nil
}

// Execute starts a new agentic loop execution for the given session.
func (e *LoopExecutor) Execute(ctx context.Context, session *Session, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	return e.runLoop(ctx, session, incoming, handler)
}

// runLoop executes the main agentic loop.
// It runs up to maxSteps iterations per trigger/resume invocation.
func (e *LoopExecutor) runLoop(ctx context.Context, session *Session, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	steps := 0

	for _, agentID := range session.WaitingAgents() {
		buffer := session.WaitingBuffer(agentID)
		_ = buffer
		// TODO(jbd): Run the agent with session history and buffer as input.
		return fmt.Errorf("resuming waiting agents is not yet supported")
	}

	// Write the new inputs to the event log.
	if err := session.WriteContent(ctx, "", incoming.CheckpointId, incoming.Contents); err != nil {
		return fmt.Errorf("failed to write input content: %w", err)
	}

	for steps < e.maxSteps {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		tasks, err := e.planFunc(ctx, session.History())
		if err != nil {
			return fmt.Errorf("planning failed: %w", err)
		}

		if len(tasks) == 0 {
			// No more tasks to execute; loop is complete.
			return nil
		}

		g, ctx := errgroup.WithContext(ctx)
		for _, task := range tasks {
			g.Go(func() error {
				return e.runTask(ctx, session, task, handler)
			})
		}
		if err := g.Wait(); err != nil {
			return err
		}

		steps++
	}

	// Can be resumed later with another trigger
	return fmt.Errorf("max steps per trigger (%d) reached", e.maxSteps)
}

func (e *LoopExecutor) runTask(ctx context.Context, session *Session, task *Task, handler agent.OutputHandler) error {
	// TODO(jbd): Log task start and task end to allow resuming dangling tasks.
	taskOutputHandler := func(outgoing *proto.ProcessResponse) error {
		if err := session.WriteContent(ctx, task.AgentID, outgoing.CheckpointId, outgoing.Contents); err != nil {
			return fmt.Errorf("failed to write output content: %w", err)
		}
		return handler(outgoing)
	}

	ag, err := e.registry.Get(task.AgentID)
	if err != nil {
		return fmt.Errorf("failed to get agent: %w", err)
	}

	if err := ag.Process(ctx, session.ID(), &proto.ProcessRequest{
		Contents: task.Inputs,
	}, taskOutputHandler); err != nil {
		return fmt.Errorf("agent process failed: %w", err)
	}
	return nil
}
