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
	"log/slog"

	"github.com/google/ax/internal/controller/eventlog"
	"github.com/google/ax/internal/harness"
	"github.com/google/ax/proto"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type ExecHandler func(resp *proto.ExecResponse) error

// Controller is the main controller that coordinates all components.
// It acts as a single-writer system for managing agentic loops.
type Controller struct {
	registry *Registry
	eventLog eventlog.EventLog
}

// Config configures the controller.
type Config struct {
	Registry        *Registry
	EventLogBuilder eventlog.EventLogBuilder
}

// New creates a new controller instance.
func New(ctx context.Context, cfg Config) (*Controller, error) {
	if cfg.Registry == nil {
		return nil, fmt.Errorf("registry is required")
	}
	if cfg.EventLogBuilder == nil {
		return nil, fmt.Errorf("event log builder is required")
	}
	eventLog, err := cfg.EventLogBuilder()
	if err != nil {
		return nil, fmt.Errorf("failed to create event log: %w", err)
	}

	return &Controller{
		registry: cfg.Registry,
		eventLog: eventLog,
	}, nil
}

// Exec executes a new agentic loop execution or resumes an existing one.
// If id is empty, a UUID will be generated.
// If the execution already exists, it will be resumed with optional new inputs.
func (d *Controller) Exec(ctx context.Context, req *proto.ExecRequest, handler ExecHandler) error {
	if req.ConversationId == "" {
		return fmt.Errorf("conversation_id is required")
	}

	// TODO(jbd): Resume an incomplete execution if there exists one.
	// TODO(jbd): Enable bringing a remote harness that implements HarnessService.
	// TODO(anj): We need to consolidate agents and harness registration.
	// Adding harness registration support temporarily.
	l := newLogger(d.eventLog, req.ConversationId, req.HarnessId)
	state, storedHarnessID, err := l.ResumptionState(ctx)
	if err != nil {
		return fmt.Errorf("failed to check resumption state: %w", err)
	}

	// On resume, use the conversation's recorded harness. Using a different harness
	// for the same conversation is not allowed.
	if req.HarnessId != "" && storedHarnessID != "" && req.HarnessId != storedHarnessID {
		return fmt.Errorf("resumption not allowed: harness ID changed from %s to %s", storedHarnessID, req.HarnessId)
	}
	harnessID := req.HarnessId
	// Use the conversations's stored harness if no harness is specified.
	if harnessID == "" {
		harnessID = storedHarnessID
	}
	// For new conversations, use the default harness if no harness is specified.
	if harnessID == "" {
		harnessID = d.registry.defaultHarness
	}
	l.harnessID = harnessID

	h, err := d.registry.Harness(harnessID)
	if err != nil {
		return fmt.Errorf("failed to get harness %q: %w", harnessID, err)
	}

	hhandler := &harnessHandler{
		logger:      l,
		execHandler: handler,
	}

	if state == proto.State_STATE_PENDING {
		// If the state is pending, first try to resume the
		// pending execution. If the state is COMPLETED or FAILED, start
		// a new execution.
		exec, err := h.Start(ctx, req.ConversationId, req.HarnessConfig)
		if err != nil {
			return fmt.Errorf("failed to start harness session: %w", err)
		}
		var runErr error
		if closeBeforeNextStart(exec) {
			runErr = func() error {
				defer exec.Close(ctx)
				return exec.Run(ctx, hhandler)
			}()
		} else {
			defer exec.Close(ctx)
			runErr = exec.Run(ctx, hhandler)
		}
		if runErr != nil {
			return fmt.Errorf("harness execution failed: %w", runErr)
		}
	}

	if len(req.Inputs) == 0 {
		// No more inputs, just return.
		return nil
	}

	exec, err := h.Start(ctx, req.ConversationId, req.HarnessConfig)
	if err != nil {
		return fmt.Errorf("failed to start harness session: %w", err)
	}
	defer exec.Close(ctx)

	if err := exec.Queue(ctx, req.Inputs...); err != nil {
		return fmt.Errorf("failed to queue inputs: %w", err)
	}
	// Log inputs before running harness
	if _, err := l.LogInputs(ctx, req.Inputs, req.HarnessConfig); err != nil {
		return fmt.Errorf("failed to log inputs: %w", err)
	}
	if err := exec.Run(ctx, hhandler); err != nil {
		return fmt.Errorf("harness execution failed: %w", err)
	}
	return nil
}

// eagerCloseExecution is an optional Execution capability. Executions whose
// harness tracks per-conversation turn state (substrate warm mode) must be
// closed before the controller starts another execution for the same
// conversation; everything else keeps upstream's deferred-close semantics.
type eagerCloseExecution interface {
	CloseBeforeNextStart() bool
}

func closeBeforeNextStart(exec harness.Execution) bool {
	ec, ok := exec.(eagerCloseExecution)
	return ok && ec.CloseBeforeNextStart()
}

type harnessHandler struct {
	logger      *logger
	execHandler ExecHandler
}

func (a *harnessHandler) OnMessage(ctx context.Context, execID string, msg *proto.Message) error {
	// Log every response received from the harness
	// TODO(anj): The harness should send the full input sent to get this particular response.
	seq, err := a.logger.LogOutputs(ctx, []*proto.Message{msg}, proto.State_STATE_PENDING, nil, "")
	if err != nil {
		slog.WarnContext(ctx, "Failed to log streamed message to event log",
			slog.String("conversation_id", a.logger.conversationID),
			slog.Any("error", err),
		)
	}

	if a.execHandler == nil {
		return nil
	}
	return a.execHandler(&proto.ExecResponse{
		Outputs: []*proto.Message{msg},
		Seq:     seq,
	})
}

func (a *harnessHandler) OnComplete(ctx context.Context, execID string) error {
	return a.complete(ctx, execID, nil)
}

// OnCompleteWithMetadata retains opaque metadata on the existing terminal
// event without expanding harness.Handler.
func (a *harnessHandler) OnCompleteWithMetadata(ctx context.Context, execID string, metadata []byte) error {
	return a.complete(ctx, execID, metadata)
}

// OnFailWithMetadata persists a terminal FAILED event that still carries the
// harness's opaque metadata (e.g. token usage collected before the failure),
// then returns the original cause unchanged so the caller's error path is
// unaffected by whether the metadata could be persisted.
func (a *harnessHandler) OnFailWithMetadata(ctx context.Context, execID string, metadata []byte, cause error) error {
	// Metadata-bearing terminal events are stamped with the stream's execID,
	// mirroring complete()'s convention for the COMPLETED path.
	terminalExecID := ""
	if len(metadata) > 0 {
		terminalExecID = execID
	}
	seq, err := a.logger.LogOutputs(ctx, nil, proto.State_STATE_FAILED, metadata, terminalExecID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to log FAILED terminal metadata",
			slog.String("conversation_id", a.logger.conversationID),
			slog.Any("error", err),
		)
		return cause
	}
	if a.execHandler != nil {
		if err := a.execHandler(&proto.ExecResponse{
			Seq:             seq,
			HarnessMetadata: metadata,
		}); err != nil {
			slog.WarnContext(ctx, "Failed to stream FAILED terminal metadata to exec handler",
				slog.String("conversation_id", a.logger.conversationID),
				slog.Any("error", err),
			)
		}
	}
	return cause
}

func (a *harnessHandler) complete(ctx context.Context, execID string, metadata []byte) error {
	// Metadata-bearing terminal events are stamped with the stream's execID;
	// the legacy no-metadata path keeps the logger's (empty) execID unchanged.
	terminalExecID := ""
	if len(metadata) > 0 {
		terminalExecID = execID
	}
	// Mark the execution turn as completed in the conversation log
	seq, err := a.logger.LogOutputs(ctx, nil, proto.State_STATE_COMPLETED, metadata, terminalExecID)
	if err != nil {
		slog.WarnContext(ctx, "Failed to log completion event to event log",
			slog.String("conversation_id", a.logger.conversationID),
			slog.Any("error", err),
		)
		if len(metadata) > 0 {
			return fmt.Errorf("failed to persist terminal harness metadata: %w", err)
		}
		return nil
	}
	if len(metadata) == 0 || a.execHandler == nil {
		return nil
	}
	return a.execHandler(&proto.ExecResponse{
		Seq:             seq,
		HarnessMetadata: metadata,
	})
}

// Delete deletes all events for a specific conversation ID.
func (d *Controller) Delete(ctx context.Context, conversationID string) error {
	if conversationID == "" {
		return fmt.Errorf("conversation_id is required")
	}

	return d.eventLog.DeleteAll(ctx, conversationID)
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

func newLogger(
	el eventlog.EventLog,
	conversationID string,
	harnessID string) *logger {
	return &logger{
		el:             el,
		conversationID: conversationID,
		harnessID:      harnessID,
	}
}

type logger struct {
	conversationID string
	execID         string
	el             eventlog.EventLog
	harnessID      string
}

// ResumptionState returns the conversation's current state and the harness it used.
func (l *logger) ResumptionState(ctx context.Context) (proto.State, string, error) {
	events, err := l.el.Events(ctx, l.conversationID)
	if err != nil {
		return proto.State_STATE_UNSPECIFIED, "", err
	}

	var state proto.State
	var harnessID string
	for _, ev := range events {
		if harnessID == "" && ev.HarnessId != "" {
			harnessID = ev.HarnessId
		}
		if l.execID == "" || ev.ExecId == l.execID {
			if ev.State != proto.State_STATE_UNSPECIFIED {
				state = ev.State
			}
		}
	}
	return state, harnessID, nil
}

func (l *logger) LogInputs(ctx context.Context, inputs []*proto.Message, harnessConfig []byte) (int32, error) {
	// Parse the harness config into a human-readable struct for logging.
	var cfg *structpb.Struct
	if len(harnessConfig) > 0 {
		cfg = &structpb.Struct{}
		if err := protojson.Unmarshal(harnessConfig, cfg); err != nil {
			slog.WarnContext(ctx, "Failed to parse harness config for logging",
				slog.String("conversation_id", l.conversationID),
				slog.Any("error", err),
			)
			cfg = nil
		}
	}
	if cfg != nil {
		// agentfleet #223: actor_token is a reserved, transport-only key in the
		// pushed harness_config — a per-conversation bearer credential the harness
		// extracts before use. It must never reach the durable conversation log.
		// Strip it from the human-readable copy only; the live bytes handed to
		// h.Start are untouched, so delivery is unaffected. Everything else in the
		// config remains fully logged for audit.
		delete(cfg.Fields, "actor_token")
	}
	ev := &proto.ConversationEvent{
		ConversationId: l.conversationID,
		ExecId:         l.execID,
		HarnessId:      l.harnessID,
		HarnessConfig:  cfg,
		Messages:       inputs,
		State:          proto.State_STATE_PENDING,
	}
	return l.el.Append(ctx, ev)
}

// LogOutputs appends an output event. A non-empty execID overrides the
// logger's own (which is never populated today) on the appended event.
func (l *logger) LogOutputs(ctx context.Context, outputs []*proto.Message, state proto.State, harnessMetadata []byte, execID string) (int32, error) {
	if execID == "" {
		execID = l.execID
	}
	ev := &proto.ConversationEvent{
		ConversationId:  l.conversationID,
		ExecId:          execID,
		Messages:        outputs,
		State:           state,
		HarnessMetadata: harnessMetadata,
	}
	return l.el.Append(ctx, ev)
}
