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
// agentic loops, manages sessions, and communicates with local and remote agents.
package controller

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/config"
	"github.com/google/gar/internal/eventlog"
	"github.com/google/gar/proto"
)

// Controller is the main controller that coordinates all components.
// It acts as a single-writer system for managing agentic loops.
type Controller struct {
	inFlightSessionsMu sync.Mutex
	inFlightSessions   map[string]struct{}
	sessionManager     *SessionManager
	registry           *Registry
	loopExecutor       *LoopExecutor
}

// PlannerBuilder is a function that creates a PlanFunc given a Registry.
type PlannerBuilder func(ctx context.Context, r *Registry) (agent.Agent, error)

// Config configures the controller.
type Config struct {
	EventLogBuilder eventlog.EventLogBuilder
	PlannerBuilder  PlannerBuilder
	// TODO(jbd): Add CompacterBuilder.
	HealthCheck config.HealthCheckConfig
	MaxSteps    int
}

// New creates a new controller instance.
func New(ctx context.Context, config Config) (*Controller, error) {
	if config.MaxSteps == 0 {
		config.MaxSteps = 5
	}

	if config.EventLogBuilder == nil {
		config.EventLogBuilder = func(sessionID string) (eventlog.EventLog, error) {
			return eventlog.NewFileEventLog(eventlog.FileConfig{
				SessionID: sessionID,
				Dir:       "eventlog",
			})
		}
	}

	// Initialize session manager with file-based event logs
	sessionManager := NewSessionManager(config.EventLogBuilder)

	// Initialize agent registry
	registry, err := NewRegistry(config.HealthCheck)
	if err != nil {
		return nil, fmt.Errorf("failed to create registry: %w", err)
	}

	// Determine plan function
	// If no planner builder is provided, use the default Gemini planner.
	if config.PlannerBuilder == nil {
		config.PlannerBuilder = func(ctx context.Context, r *Registry) (agent.Agent, error) {
			return NewGeminiPlanner(ctx, r, GeminiPlannerConfig{})
		}
	}

	planner, err := config.PlannerBuilder(ctx, registry)
	if err != nil {
		return nil, fmt.Errorf("failed to create planner from builder: %w", err)
	}

	// Initialize loop executor
	loopExecutor, err := NewLoopExecutor(ctx, LoopConfig{
		Registry:       registry,
		SessionManager: sessionManager,
		MaxSteps:       config.MaxSteps,
		Planner:        planner,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create loop executor: %w", err)
	}

	return &Controller{
		inFlightSessions: make(map[string]struct{}),
		sessionManager:   sessionManager,
		registry:         registry,
		loopExecutor:     loopExecutor,
	}, nil
}

// TriggerSession triggers a new agentic loop session or resumes an existing one.
// If sessionID is empty, a UUID will be generated.
// If the session already exists, it will be resumed with optional new inputs.
func (d *Controller) TriggerSession(ctx context.Context, sessionID string, incoming *proto.ProcessRequest, handler agent.OutputHandler) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	d.inFlightSessionsMu.Lock()
	_, ok := d.inFlightSessions[sessionID]
	if !ok {
		d.inFlightSessions[sessionID] = struct{}{}
	}
	d.inFlightSessionsMu.Unlock()

	if ok {
		return fmt.Errorf("session is already in flight")
	}

	defer func() {
		d.inFlightSessionsMu.Lock()
		delete(d.inFlightSessions, sessionID)
		d.inFlightSessionsMu.Unlock()
	}()

	// Check if session already exists
	sess, err := d.sessionManager.LoadSession(ctx, sessionID)
	if err == nil && sess == nil {
		// Session doesn't exist - create new session
		// Checkpoint ID is ignored for new sessions
		sess, err = d.sessionManager.NewSession(sessionID)
		if err != nil {
			return fmt.Errorf("failed to create session: %w", err)
		}
	}

	if sess.State() == proto.State_STATE_FAILED {
		return fmt.Errorf("session has failed and cannot continue")
	}

	if err := d.loopExecutor.Execute(ctx, sess, incoming, handler); err != nil {
		return fmt.Errorf("loop execution failed: %w", err)
	}
	return nil
}

// ForkSession forks a session from a source session.
// If checkpointId is provided, fork til the checkpoint. Otherwise, fork the whole session.
func (d *Controller) ForkSession(ctx context.Context, sourceSessionID, sourceCheckpoint, destSessionID string) error {
	if sourceSessionID == "" {
		return fmt.Errorf("source session ID is required")
	}
	if destSessionID == "" {
		return fmt.Errorf("destination session ID is required")
	}

	d.inFlightSessionsMu.Lock()
	_, ok := d.inFlightSessions[destSessionID]
	if !ok {
		d.inFlightSessions[destSessionID] = struct{}{}
	}
	d.inFlightSessionsMu.Unlock()

	if ok {
		return fmt.Errorf("newly generated session ID collision")
	}

	defer func() {
		d.inFlightSessionsMu.Lock()
		delete(d.inFlightSessions, destSessionID)
		d.inFlightSessionsMu.Unlock()
	}()

	// Fork the session
	_, err := d.sessionManager.ForkSession(ctx, sourceSessionID, sourceCheckpoint, destSessionID)
	if err != nil {
		return fmt.Errorf("failed to fork session: %w", err)
	}

	return nil
}

// LoadSession loads a session from event log.
func (d *Controller) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
	return d.sessionManager.LoadSession(ctx, sessionID)
}

// Registry returns the agent registry.
func (d *Controller) Registry() *Registry {
	return d.registry
}

// Close gracefully shuts down the controller.
func (d *Controller) Close() error {
	if err := d.registry.Close(); err != nil {
		return fmt.Errorf("failed to close registry: %w", err)
	}
	d.sessionManager.CloseAll()
	return nil
}
