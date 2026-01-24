package controller

import (
	"context"
	"fmt"

	"github.com/google/gar/agent"
	"github.com/google/gar/proto"
)

// Goal represents the objective of an agent task.
type Goal struct {
	Description string
}

// Task represents a task to be executed by an agent.
type Task struct {
	AgentID   string
	Input     []*proto.Content
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
type PlanFunc func(session *Session) (*Task, error)

// EvaluateFunc evaluates the agent's response to determine if the goal is achieved.
// Returns true if the goal is met and the loop should terminate.
type EvaluateFunc func(session *Session, task *Task, output []*proto.Content) (bool, error)

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
func (e *LoopExecutor) Execute(ctx context.Context, sessionID string, inputs []*proto.Content) error {
	// Get or create session
	session, err := e.sessionManager.GetSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to get session: %w", err)
	}

	// Write input content to session
	for _, content := range inputs {
		if _, err := session.WriteContentIn(ctx, content); err != nil {
			return fmt.Errorf("failed to write input content: %w", err)
		}
	}

	return e.runLoop(ctx, session)
}

// Resume continues an existing session from its last checkpoint.
func (e *LoopExecutor) Resume(ctx context.Context, sessionID string) error {
	// Load session from event log
	session, err := e.sessionManager.LoadSession(sessionID)
	if err != nil {
		return fmt.Errorf("failed to load session: %w", err)
	}

	// Check if session is in a resumable state
	if session.State == proto.State_STATE_COMPLETED {
		return fmt.Errorf("session already completed")
	}
	if session.State == proto.State_STATE_FAILED {
		return fmt.Errorf("session failed and cannot be resumed")
	}

	// Update state to running
	session.SetState(proto.State_STATE_RUNNING)

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
			session.SetState(proto.State_STATE_FAILED)
			return ctx.Err()
		default:
		}

		// Phase 1: Plan - Determine next agent and action
		task, err := e.planFunc(session)
		if err != nil {
			session.SetState(proto.State_STATE_FAILED)
			return fmt.Errorf("planning failed: %w", err)
		}

		// If no task, we're done
		if task == nil {
			session.SetState(proto.State_STATE_COMPLETED)
			return nil
		}

		// Get the agent from registry
		ag, err := e.registry.Get(task.AgentID)
		if err != nil {
			session.SetState(proto.State_STATE_FAILED)
			return fmt.Errorf("failed to get agent: %w", err)
		}

		// Phase 2: Execute - Send content to agent and receive response
		output, err := e.executeTask(ctx, session, ag, task)
		if err != nil {
			session.SetState(proto.State_STATE_FAILED)
			return fmt.Errorf("execution failed: %w", err)
		}

		// Phase 3: Evaluate - Check if goal achieved
		goalAchieved, err := e.evaluateFunc(session, task, output)
		if err != nil {
			session.SetState(proto.State_STATE_FAILED)
			return fmt.Errorf("evaluation failed: %w", err)
		}

		// Phase 4: Advance step counters
		session.AdvanceStep()
		steps++

		// If goal achieved, complete the session
		if goalAchieved {
			session.SetState(proto.State_STATE_COMPLETED)
			return nil
		}
	}

	// Can be resumed later with another trigger
	return fmt.Errorf("max steps per trigger (%d) reached", e.maxSteps)
}

// executeTask sends input to an agent and collects output.
func (e *LoopExecutor) executeTask(ctx context.Context, session *Session, ag agent.Agent, task *Task) ([]*proto.Content, error) {
	var output []*proto.Content

	// Define output handler to collect responses
	outputHandler := func(content *proto.Content) error {
		output = append(output, content)
		if _, err := session.WriteContentOut(ctx, content); err != nil {
			return fmt.Errorf("failed to write output: %w", err)
		}
		return nil
	}

	// Start lifecycle event monitoring in background
	lifecycleCtx, cancelLifecycle := context.WithCancel(ctx)
	defer cancelLifecycle()

	lifecycleHandler := func(event *proto.LifecycleEvent) error {
		// TODO(jbd): Handle lifecycle events.
		return nil
	}

	// Start lifecycle monitoring in background goroutine
	lifecycleErr := make(chan error, 1)
	go func() {
		lifecycleErr <- ag.StreamLifecycle(lifecycleCtx, lifecycleHandler)
	}()

	// Process inputs with the agent
	if err := ag.Process(ctx, session.ID, task.Input, outputHandler); err != nil {
		cancelLifecycle()
		return nil, fmt.Errorf("agent process failed: %w", err)
	}

	// Cancel lifecycle monitoring
	cancelLifecycle()

	return output, nil
}

// defaultEvaluateFunc is a simple default evaluation function.
// It considers the goal achieved after processing one step.
func defaultEvaluateFunc(session *Session, task *Task, output []*proto.Content) (bool, error) {
	// Simple evaluation: goal achieved if we got any output
	return len(output) > 0, nil
}
