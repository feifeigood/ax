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

package a2abridge

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/google/ax/internal/agent"
	"github.com/google/ax/proto"
)

type streamState struct {
	conversationID  string
	toolCallID      string // planner's tool call ID
	agentID         string // for AuthRequiredError
	o               agent.OutputHandler
	activeTaskID    a2a.TaskID
	terminal        bool
	lastSeenTask    *a2a.Task
	artifactBuffer  map[a2a.ArtifactID]*a2a.Artifact
	emittedArtifact map[a2a.ArtifactID]bool
}

func newStreamState(conversationID, toolCallID, agentID string, o agent.OutputHandler) *streamState {
	return &streamState{
		conversationID:  conversationID,
		toolCallID:      toolCallID,
		agentID:         agentID,
		o:               o,
		artifactBuffer:  make(map[a2a.ArtifactID]*a2a.Artifact),
		emittedArtifact: make(map[a2a.ArtifactID]bool),
	}
}

// handleEvent dispatches a single event from the A2A stream. Returns
// (stop, err): stop indicates the stream should end (e.g. HITL pause).
func (s *streamState) handleEvent(evt a2a.Event) (bool, error) {
	switch e := evt.(type) {
	case *a2a.Message:
		// Direct message response (no task).
		msgs := a2aPartsToMessages(e.Parts, a2aRoleToRole(e.Role))
		if len(msgs) > 0 {
			if err := s.o(&proto.AgentOutputs{Messages: msgs}); err != nil {
				return false, err
			}
		}
		return false, nil

	case *a2a.Task:
		// Snapshot of the task. Track it; emit any artifacts we haven't
		// seen yet. If the task is in INPUT_REQUIRED, treat it the same
		// way as the corresponding TaskStatusUpdateEvent: surface a
		// ConfirmationContent and persist the activeTaskID, then stop
		// the loop.
		s.lastSeenTask = e
		if e.ID != "" {
			s.activeTaskID = e.ID
		}
		for _, art := range e.Artifacts {
			if s.emittedArtifact[art.ID] {
				continue
			}
			s.emittedArtifact[art.ID] = true
			msgs := a2aArtifactToMessages(art, "agent")
			if len(msgs) > 0 {
				if err := s.o(&proto.AgentOutputs{Messages: msgs}); err != nil {
					return false, err
				}
			}
		}
		if e.Status.State == a2a.TaskStateAuthRequired {
			// Server is asking for refreshed credentials. The current static
			// credential model can't hot-reload, so mark terminal,
			// clear the marker, and return an actionable error.
			s.terminal = true
			_ = ClearStateMarker(s.conversationID, s.o)
			return false, AuthRequiredError(s.agentID, s.activeTaskID, e.Status.Message)
		}
		if e.Status.State == a2a.TaskStateInputRequired {
			if err := EmitConfirmation(s.conversationID, s.toolCallID, s.activeTaskID, e, emittedArtifactKeys(s.emittedArtifact), s.o); err != nil {
				return false, err
			}
			return true, nil
		}
		if e.Status.State.Terminal() {
			s.terminal = true
		}
		return false, nil

	case *a2a.TaskStatusUpdateEvent:
		if e.TaskID != "" {
			s.activeTaskID = e.TaskID
		}
		// Surface any human-readable status message to the client - EXCEPT
		// when the status is input_required (delivered as
		// ConfirmationContent.Question) or auth_required (delivered as
		// part of AuthRequiredError).
		if e.Status.Message != nil &&
			e.Status.State != a2a.TaskStateInputRequired &&
			e.Status.State != a2a.TaskStateAuthRequired {
			msgs := a2aPartsToMessages(e.Status.Message.Parts, a2aRoleToRole(e.Status.Message.Role))
			if len(msgs) > 0 {
				if err := s.o(&proto.AgentOutputs{Messages: msgs}); err != nil {
					return false, err
				}
			}
		}
		switch e.Status.State {
		case a2a.TaskStateInputRequired:
			// HITL pause: persist the active task ID and emit a
			// ConfirmationContent. Stop the stream loop.
			task := &a2a.Task{
				ID:        s.activeTaskID,
				ContextID: s.conversationID,
				Status:    e.Status,
			}
			if err := EmitConfirmation(s.conversationID, s.toolCallID, s.activeTaskID, task, emittedArtifactKeys(s.emittedArtifact), s.o); err != nil {
				return false, err
			}
			return true, nil
		case a2a.TaskStateAuthRequired:
			// Server is asking for refreshed credentials mid-stream.
			// Our static-credential model can't hot-reload, so mark
			// terminal, clear the marker, and return an actionable error.
			s.terminal = true
			_ = ClearStateMarker(s.conversationID, s.o)
			return false, AuthRequiredError(s.agentID, s.activeTaskID, e.Status.Message)
		case a2a.TaskStateCompleted:
			s.terminal = true
			if err := ClearStateMarker(s.conversationID, s.o); err != nil {
				return false, err
			}
		case a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
			s.terminal = true
			_ = ClearStateMarker(s.conversationID, s.o)
			return false, fmt.Errorf("a2a task %s ended with state %s", s.activeTaskID, e.Status.State)
		}
		return false, nil

	case *a2a.TaskArtifactUpdateEvent:
		if e.TaskID != "" {
			s.activeTaskID = e.TaskID
		}
		if e.Artifact == nil {
			return false, nil
		}
		// Buffer chunks by artifact ID; emit on LastChunk=true. We rely
		// on the A2A protocol's LastChunk contract and don't add a
		// per-artifact timeout.
		existing, ok := s.artifactBuffer[e.Artifact.ID]
		if !ok || !e.Append {
			// Start a new buffer entry (or replace if not appending).
			cp := *e.Artifact
			s.artifactBuffer[e.Artifact.ID] = &cp
		} else {
			existing.Parts = append(existing.Parts, e.Artifact.Parts...)
		}
		if e.LastChunk {
			full := s.artifactBuffer[e.Artifact.ID]
			delete(s.artifactBuffer, e.Artifact.ID)
			s.emittedArtifact[e.Artifact.ID] = true
			msgs := a2aArtifactToMessages(full, "agent")
			if len(msgs) > 0 {
				if err := s.o(&proto.AgentOutputs{Messages: msgs}); err != nil {
					return false, err
				}
			}
		}
		return false, nil
	}
	return false, nil
}

// IterateStream walks the SDK's iter.Seq2 stream of events, dispatches
// each to the appropriate handler, and returns once the stream ends or a
// fatal error is encountered. If the stream ends with the active task
// still in a non-terminal state, falls back to polling GetTask via
// PollUntilTerminal until the task reaches a terminal state.
//
// client and agentID are passed through to the polling fallback so it
// can issue GetTask calls and format error messages.
func IterateStream(
	ctx context.Context,
	client *a2aclient.Client,
	agentID string,
	conversationID string,
	toolCallID string,
	events iter.Seq2[a2a.Event, error],
	o agent.OutputHandler,
) error {
	state := newStreamState(conversationID, toolCallID, agentID, o)
	for evt, err := range events {
		if err != nil {
			return fmt.Errorf("a2a agent %s: stream error: %w", agentID, err)
		}
		stop, err := state.handleEvent(evt)
		if err != nil {
			return err
		}
		if stop {
			return nil
		}
	}
	// Stream ended. If we have an active task that's still non-terminal
	// the server expects us to poll for completion.
	if state.activeTaskID != "" && !state.terminal {
		return PollUntilTerminal(ctx, client, agentID, conversationID, toolCallID, state.activeTaskID, state.lastSeenTask, emittedArtifactKeys(state.emittedArtifact), o)
	}
	return nil
}

// PollUntilTerminal polls GetTask with exponential backoff until the task
// reaches a terminal state, the context is canceled, or an error occurs.
// Newly discovered artifacts and status changes are emitted to the client.
//
// client and agentID identify the SDK client and the bridge's registered
// agent ID for issuing GetTask and constructing user-facing errors.
//
// toolCallID is forwarded to EmitConfirmation if the task transitions to
// INPUT_REQUIRED while we are polling.
//
// alreadyEmitted seeds the dedup set with artifact IDs already emitted by
// the caller. It prevents re-emission across the streaming<->polling handoff
// and across invocation boundaries.
func PollUntilTerminal(
	ctx context.Context,
	client *a2aclient.Client,
	agentID string,
	conversationID string,
	toolCallID string,
	taskID a2a.TaskID,
	lastSeen *a2a.Task,
	alreadyEmitted []string,
	o agent.OutputHandler,
) error {
	emittedArtifactIDs := make(map[a2a.ArtifactID]bool)
	if lastSeen != nil {
		for _, art := range lastSeen.Artifacts {
			emittedArtifactIDs[art.ID] = true
		}
	}
	for _, id := range alreadyEmitted {
		emittedArtifactIDs[a2a.ArtifactID(id)] = true
	}

	const initialDelay = 250 * time.Millisecond
	const maxDelay = 60 * time.Second

	delay := initialDelay
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}

		task, err := client.GetTask(ctx, &a2a.GetTaskRequest{ID: taskID})
		if err != nil {
			// If the server has dropped the task while we were polling,
			// the marker is stale: clear it and surface a clean error.
			if errors.Is(err, a2a.ErrTaskNotFound) {
				_ = ClearStateMarker(conversationID, o)
				return fmt.Errorf("a2a agent %s: task %s no longer exists on server (state marker cleared)", agentID, taskID)
			}
			return fmt.Errorf("a2a agent %s: poll GetTask(%s): %w", agentID, taskID, err)
		}

		// Emit newly observed artifacts. Track whether anything new
		// arrived this iteration.
		sawNewArtifact := false
		for _, art := range task.Artifacts {
			if emittedArtifactIDs[art.ID] {
				continue
			}
			emittedArtifactIDs[art.ID] = true
			sawNewArtifact = true
			msgs := a2aArtifactToMessages(art, "agent")
			if len(msgs) > 0 {
				if err := o(&proto.AgentOutputs{Messages: msgs}); err != nil {
					return err
				}
			}
		}

		switch task.Status.State {
		case a2a.TaskStateCompleted:
			return ClearStateMarker(conversationID, o)
		case a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
			_ = ClearStateMarker(conversationID, o)
			return fmt.Errorf("a2a agent %s: task %s ended with state %s", agentID, taskID, task.Status.State)
		case a2a.TaskStateAuthRequired:
			// Server is asking for refreshed credentials mid-poll.
			// Our static-credential model can't hot-reload, so clear
			// the marker and surface an actionable error.
			_ = ClearStateMarker(conversationID, o)
			return AuthRequiredError(agentID, taskID, task.Status.Message)
		case a2a.TaskStateInputRequired:
			// Convert to HITL pause; persist the current emitted set
			// so resume after the user's answer doesn't re-emit.
			return EmitConfirmation(conversationID, toolCallID, taskID, task, emittedArtifactKeys(emittedArtifactIDs), o)
		}

		// Reset poll delay on activity.
		if sawNewArtifact {
			delay = initialDelay
		} else {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

// EmitTaskArtifacts emits all artifacts from a completed task to the client.
func EmitTaskArtifacts(task *a2a.Task, o agent.OutputHandler) error {
	for _, art := range task.Artifacts {
		msgs := a2aArtifactToMessages(art, "agent")
		if len(msgs) > 0 {
			if err := o(&proto.AgentOutputs{Messages: msgs}); err != nil {
				return err
			}
		}
	}
	return nil
}

// EmitConfirmation surfaces a HITL prompt to the AX client and persists the
// state marker so the bridge can resume the same task on the user's reply.
//
// ConfirmationContentID is set to toolCallID so the planner can correlate
// the user's reply back to the call via a strict ID match.
// If toolCallID is empty (the bridge was invoked without a planner), we fall
// back to string(taskID).
func EmitConfirmation(conversationID, toolCallID string, taskID a2a.TaskID, task *a2a.Task, emittedArtifactIDs []string, o agent.OutputHandler) error {
	question := "The agent requires confirmation."
	if task != nil && task.Status.Message != nil {
		var b string
		for _, part := range task.Status.Message.Parts {
			if t := part.Text(); t != "" {
				if b != "" {
					b += "\n"
				}
				b += t
			}
		}
		if b != "" {
			question = b
		}
	}
	// Persist the (conversationID, taskID, emittedArtifactIDs) first so
	// resume can recover it.
	stateMsg, err := marshalA2AState(conversationID, string(taskID), emittedArtifactIDs)
	if err != nil {
		return err
	}
	if err := o(&proto.AgentOutputs{Messages: []*proto.Message{stateMsg}}); err != nil {
		return err
	}
	confID := toolCallID
	if confID == "" {
		confID = string(taskID)
	}
	// Emit the ConfirmationContent last so the executor sees it as the
	// final output and records the exec as PENDING.
	confirmMsg := &proto.Message{
		Role: "agent",
		Content: &proto.Content{
			Type: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id:       confID,
					Question: question,
				},
			},
		},
	}
	return o(&proto.AgentOutputs{Messages: []*proto.Message{confirmMsg}})
}

// AuthRequiredError formats a user-facing error for TaskStateAuthRequired.
// Surfaces any hint the agent included in Status.Message. Temporary for now.
func AuthRequiredError(agentID string, taskID a2a.TaskID, statusMsg *a2a.Message) error {
	var b strings.Builder
	fmt.Fprintf(&b, "a2a agent %s: task %s requires updated auth credentials", agentID, taskID)
	if statusMsg != nil {
		var hint string
		for _, part := range statusMsg.Parts {
			if t := part.Text(); t != "" {
				if hint != "" {
					hint += "\n"
				}
				hint += t
			}
		}
		if hint != "" {
			fmt.Fprintf(&b, ": %s", hint)
		}
	}
	b.WriteString("; refresh credentials and restart AX")
	return errors.New(b.String())
}

// ClearStateMarker writes a final state marker indicating no active task.
func ClearStateMarker(conversationID string, o agent.OutputHandler) error {
	msg, err := marshalA2AState(conversationID, "", nil)
	if err != nil {
		return err
	}
	return o(&proto.AgentOutputs{Messages: []*proto.Message{msg}})
}

// FindConfirmationAnswer scans the history (most recent first) for a user
// reply to the ConfirmationContent we emitted with the given confID.
// Returns (true, approved) if a matching answer is found, (false, false)
// otherwise.
func FindConfirmationAnswer(history []*proto.Message, confID string) (answered, approved bool) {
	for i := len(history) - 1; i >= 0; i-- {
		conf := history[i].GetContent().GetConfirmation()
		if conf == nil {
			continue
		}
		if confID != "" && conf.Id != confID {
			continue
		}
		if conf.GetApproval() != nil {
			return true, true
		}
		if conf.GetDecline() != nil {
			return true, false
		}
	}
	return false, false
}

// FindToolCallID returns the ID of the most recent ToolCall in history
// whose function name matches agentID.
// Returns "" when no matching ToolCall is found - e.g. the bridge was
// invoked without a planner.
func FindToolCallID(agentID string, messages []*proto.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		tc := messages[i].GetContent().GetToolCall()
		if tc == nil {
			continue
		}
		fc := tc.GetFunctionCall()
		if fc == nil || fc.Name != agentID {
			continue
		}
		return tc.Id
	}
	return ""
}
