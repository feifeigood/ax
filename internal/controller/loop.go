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

	"github.com/google/gar/proto"
)

// Goal represents the objective of an agent task.
type Goal struct {
	Description string
}

// Task represents a task to be executed by an agent.
type Task struct {
	AgentID   string
	Inputs    []*proto.Content
	Goal      *Goal
	StepIndex int
}

// LoopExecutor orchestrates the agentic loop workflow.
// It implements the plan-execute-evaluate-checkpoint cycle.
type LoopExecutor struct {
	registry       *Registry
	sessionManager *SessionManager
	maxSteps       int
	planFunc       PlanFunc
	evaluateFunc   EvaluateFunc
}

// PlanFunc determines the next agent task to execute.
// It receives the current session state and returns the next task.
type PlanFunc func(ctx context.Context, session *Session) (*Task, error)

// EvaluateFunc evaluates the agent's response to determine if the goal is achieved.
// Returns true if the goal is met and the loop should terminate.
type EvaluateFunc func(ctx context.Context, session *Session, task *Task, output []*proto.Content) (bool, error)

// LoopConfig configures the loop executor.
type LoopConfig struct {
	Registry       *Registry
	SessionManager *SessionManager
	MaxSteps       int
	PlanFunc       PlanFunc
	EvaluateFunc   EvaluateFunc
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
		config.MaxSteps = 100 // Default max steps per trigger
	}

	// Provide default plan function if not specified
	if config.PlanFunc == nil {
		// Use Gemini planner by default
		geminiPlanFunc, err := NewGeminiPlanFunc(ctx, config.Registry, GeminiPlannerConfig{})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize default Gemini planner: %w (set GEMINI_API_KEY env var or provide custom PlanFunc)", err)
		}
		config.PlanFunc = geminiPlanFunc
	}

	// Provide default evaluate function if not specified
	if config.EvaluateFunc == nil {
		config.EvaluateFunc = defaultEvaluateFunc
	}

	return &LoopExecutor{
		registry:       config.Registry,
		sessionManager: config.SessionManager,
		maxSteps:       config.MaxSteps,
		planFunc:       config.PlanFunc,
		evaluateFunc:   config.EvaluateFunc,
	}, nil
}

// Execute starts a new agentic loop execution for the given session.
func (e *LoopExecutor) Execute(ctx context.Context, session *Session, inputs []*proto.Content) error {
	if session.State() == proto.State_STATE_FAILED {
		return fmt.Errorf("session has failed and cannot continue")
	}
	if err := session.SetState(ctx, proto.State_STATE_RUNNING); err != nil {
		return fmt.Errorf("failed to set session state: %w", err)
	}

	// Write input content to session
	for _, content := range inputs {
		if _, err := session.WriteContentIn(ctx, content); err != nil {
			return fmt.Errorf("failed to write input content: %w", err)
		}
	}
	return e.runLoop(ctx, session)
}

// runLoop executes the main agentic loop.
// It runs up to maxSteps iterations per trigger/resume invocation.
func (e *LoopExecutor) runLoop(ctx context.Context, session *Session) error {
	steps := 0

	for steps < e.maxSteps {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// Phase 1: Plan - Determine next agent and action
		task, err := e.planFunc(ctx, session)
		if err != nil {
			return fmt.Errorf("planning failed: %w", err)
		}

		// If no task, we're done
		if task == nil {
			return nil
		}

		// Phase 2: Execute - Send content to agent and receive response
		outputs, err := e.executeTask(ctx, session, task)
		if err != nil {
			return fmt.Errorf("execution failed: %w", err)
		}

		for _, output := range outputs {
			if _, err := session.WriteContentOut(ctx, output); err != nil {
				return fmt.Errorf("failed to write task inputs: %w", err)
			}
		}

		// Phase 3: Evaluate - Check if goal achieved
		goalAchieved, err := e.evaluateFunc(ctx, session, task, outputs)
		if err != nil {
			return fmt.Errorf("evaluation failed: %w", err)
		}

		// Phase 4: Advance step counters
		steps++

		// If goal achieved, complete the session
		if goalAchieved {
			return nil
		}
	}

	// Can be resumed later with another trigger
	return fmt.Errorf("max steps per trigger (%d) reached", e.maxSteps)
}

// executeTask sends input to an agent and collects output.
func (e *LoopExecutor) executeTask(ctx context.Context, session *Session, task *Task) ([]*proto.Content, error) {
	// Get the agent from registry
	ag, err := e.registry.Get(task.AgentID)
	if err != nil {
		return nil, fmt.Errorf("failed to get agent: %w", err)
	}

	var outputs []*proto.Content

	// Define output handler to collect responses
	outputHandler := func(content *proto.Content) error {
		outputs = append(outputs, content)

		return nil
	}

	// Process inputs with the agent
	if err := ag.Process(ctx, session.ID, task.Inputs, outputHandler); err != nil {
		return nil, fmt.Errorf("agent process failed: %w", err)
	}

	return outputs, nil
}

// defaultEvaluateFunc is a simple default evaluation function.
// It considers the goal achieved after processing one step.
func defaultEvaluateFunc(ctx context.Context, session *Session, task *Task, output []*proto.Content) (bool, error) {
	// Simple evaluation: goal achieved if we got any output
	return len(output) > 0, nil
}
