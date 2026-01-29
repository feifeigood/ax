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
	"time"

	"github.com/google/gar/agent"
	"github.com/google/gar/internal/eventlog"
	"github.com/google/gar/proto"
)

// Controller is the main controller that coordinates all components.
// It acts as a single-writer system for managing agentic loops.
type Controller struct {
	inFlightSessionsMu sync.Mutex
	inFlightSessions   map[string]struct{}

	sessionManager *SessionManager
	registry       *Registry
	loopExecutor   *LoopExecutor
}

// Config configures the controller.
type Config struct {
	EventLogFactory eventlog.EventLogFactory
	PlanFunc        PlanFunc
	EvaluateFunc    EvaluateFunc

	// TODO(jbd): Add CompactionFunc.

	HealthCheckInterval time.Duration
	MaxSteps            int
}

// New creates a new controller instance.
func New(ctx context.Context, config Config) (*Controller, error) {
	if config.MaxSteps == 0 {
		config.MaxSteps = 100
	}
	if config.HealthCheckInterval == 0 {
		config.HealthCheckInterval = 30 * time.Second
	}

	// Initialize session manager with file-based event logs
	sessionManager := NewSessionManager(config.EventLogFactory)

	// Initialize agent registry
	registry := NewRegistry(config.HealthCheckInterval)

	// Initialize loop executor
	loopExecutor, err := NewLoopExecutor(ctx, LoopConfig{
		Registry:       registry,
		SessionManager: sessionManager,
		MaxSteps:       config.MaxSteps,
		PlanFunc:       config.PlanFunc,
		EvaluateFunc:   config.EvaluateFunc,
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
// If checkpointID is provided, resumes from that specific checkpoint instead of the latest.
func (d *Controller) TriggerSession(ctx context.Context, sessionID string, inputs []*proto.Content, handler agent.OutputHandler) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}

	d.inFlightSessionsMu.Lock()
	_, ok := d.inFlightSessions[sessionID]
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
	if err := d.loopExecutor.Execute(ctx, sess, inputs); err != nil {
		return fmt.Errorf("execution failed: %w", err)
	}
	return nil
}

func (d *Controller) TriggerForkedSession(ctx context.Context, sessionID string, checkpointID string, inputs []*proto.Content, handler agent.OutputHandler) error {
	if sessionID == "" {
		return fmt.Errorf("session_id is required")
	}
	if checkpointID == "" {
		return fmt.Errorf("checkpoint_id is required")
	}
	// TODO(jbd): Fork a new session by copying all content to
	// the provided checkpoint ID, and execute the loop executor.
	panic("not yet implemented")
}

// LoadSession loads a session from event log.
func (d *Controller) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
	return d.sessionManager.LoadSession(ctx, sessionID)
}

// CloseSession closes a session.
func (d *Controller) CloseSession(ctx context.Context, sessionID string) error {
	return d.sessionManager.CloseSession(sessionID)
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
