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
	"errors"
	"fmt"
	"strings"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/auth"
	"github.com/google/ax/internal/experimental/a2abridge"
	"github.com/google/ax/proto"
)

// A2AAgentConfig is the per-agent configuration needed to construct an
// A2AAgent.
//
// Stateless controls how the bridge composes outgoing A2A messages:
//
//   - false (default, "stateful"): the bridge sends only the new user
//     input each turn and relies on the agent server using contextId to
//     load conversation history from its own session store. This matches
//     the A2A protocol convention.
//
//   - true: the bridge sends the full AX message history on every call.
//     Use this when targeting an A2A agent that does not maintain its
//     own per-context state.
type A2AAgentConfig struct {
	ID                string
	Address           string
	Auth              auth.Auth
	Headers           auth.Headers
	Stateless         bool
	OverrideCardHosts bool
}

// A2AAgent bridges AX's Agent interface to an A2A-protocol agent.
type A2AAgent struct {
	id        string
	client    *a2aclient.Client
	card      *a2a.AgentCard
	stateless bool
}

func NewA2AAgent(ctx context.Context, config A2AAgentConfig) (*A2AAgent, error) {
	if config.Address == "" {
		return nil, errors.New("a2a agent: address is required")
	}

	addr := config.Address
	if !strings.Contains(addr, "://") {
		addr = "http://" + addr
	}

	card, err := agentcard.DefaultResolver.Resolve(ctx, addr)
	if err != nil {
		return nil, fmt.Errorf("a2a agent %s: resolve AgentCard: %w", config.ID, err)
	}

	if config.OverrideCardHosts {
		if err := a2abridge.OverrideCardHosts(card, addr); err != nil {
			return nil, fmt.Errorf("a2a agent %s: %w", config.ID, err)
		}
	}

	interceptor, err := a2abridge.NewInterceptor(card, config.Auth, config.Headers)
	if err != nil {
		return nil, fmt.Errorf("a2a agent %s: %w", config.ID, err)
	}

	var opts []a2aclient.FactoryOption
	if interceptor != nil {
		opts = append(opts, a2aclient.WithCallInterceptors(interceptor))
	}

	client, err := a2aclient.NewFromCard(ctx, card, opts...)
	if err != nil {
		return nil, fmt.Errorf("a2a agent %s: create client: %w", config.ID, err)
	}

	return &A2AAgent{
		id:        config.ID,
		client:    client,
		card:      card,
		stateless: config.Stateless,
	}, nil
}

// Card returns the resolved AgentCard for this agent.
func (a *A2AAgent) Card() *a2a.AgentCard {
	return a.card
}

// Connect bridges one AX execution to one A2A operation.
//
// Two paths:
//
//  1. Resume an existing in-flight task.
//     This covers HITL continuation: the user has supplied an answer to a
//     ConfirmationContent we previously emitted.
//
//  2. New A2A operation.
func (a *A2AAgent) Connect(
	ctx context.Context,
	conversationID string,
	execID string,
	start *proto.AgentStart,
	e agent.Executor,
	o agent.OutputHandler,
) error {
	// Compute the planner's ToolCallID for this delegation.
	// Empty when the A2A agent is invoked directly.
	toolCallID := a2abridge.FindToolCallID(a.id, start.Messages)

	// Recover any persisted state from prior calls on this exec_id.
	markerConv, activeTaskID, recoveredEmitted, found := a2abridge.RecoverA2AState(start.Messages)
	if found && markerConv != conversationID {
		found = false
		activeTaskID = ""
		recoveredEmitted = nil
	}

	if found && activeTaskID != "" {
		return a.resumeTask(ctx, conversationID, toolCallID, a2a.TaskID(activeTaskID), recoveredEmitted, start, o)
	}
	return a.newOperation(ctx, conversationID, toolCallID, start, o)
}

// Resumes an in-flight task. It checks the server-side state via GetTask
// and either continues a HITL flow, picks up completed artifacts, surfaces
// a failure, or resumes streaming for a still-active task.
func (a *A2AAgent) resumeTask(
	ctx context.Context,
	conversationID string,
	toolCallID string,
	taskID a2a.TaskID,
	recoveredEmitted []string,
	start *proto.AgentStart,
	o agent.OutputHandler,
) error {
	task, err := a.client.GetTask(ctx, &a2a.GetTaskRequest{ID: taskID})
	if err != nil {
		// If the server has dropped the task (TTL, admin action, etc.)
		// the marker is stale: clear it so the next exec starts fresh,
		// then surface the situation as an explicit error.
		if errors.Is(err, a2a.ErrTaskNotFound) {
			_ = a2abridge.ClearStateMarker(conversationID, o)
			return fmt.Errorf("a2a agent %s: task %s no longer exists on server (state marker cleared; retry to start a new operation)", a.id, taskID)
		}
		return fmt.Errorf("a2a agent %s: GetTask(%s): %w", a.id, taskID, err)
	}

	switch task.Status.State {
	case a2a.TaskStateCompleted:
		// Task already done on the server side; emit any artifacts the
		// client hasn't seen yet, then drop the now-stale marker.
		if err := a2abridge.EmitTaskArtifacts(task, o); err != nil {
			return err
		}
		return a2abridge.ClearStateMarker(conversationID, o)

	case a2a.TaskStateFailed, a2a.TaskStateCanceled, a2a.TaskStateRejected:
		_ = a2abridge.ClearStateMarker(conversationID, o)
		return fmt.Errorf("a2a agent %s: task %s ended with state %s", a.id, taskID, task.Status.State)

	case a2a.TaskStateAuthRequired:
		// Server is asking for refreshed credentials. Now returns an error.
		// TODO(wjjclaud): implement credential refresh.
		_ = a2abridge.ClearStateMarker(conversationID, o)
		return a2abridge.AuthRequiredError(a.id, taskID, task.Status.Message)

	case a2a.TaskStateInputRequired:
		// HITL: scan the resumed history for an answer to the
		// confirmation we previously emitted. If the user has answered,
		// forward to the same A2A task; if not, re-emit the confirmation
		// prompt so the client sees it again.
		matchID := toolCallID
		if matchID == "" {
			matchID = string(taskID)
		}
		answered, approved := a2abridge.FindConfirmationAnswer(start.Messages, matchID)
		if !answered {
			return a2abridge.EmitConfirmation(conversationID, toolCallID, taskID, task, recoveredEmitted, o)
		}
		return a.sendFollowUp(ctx, conversationID, toolCallID, taskID, approved, o)

	default:
		// Working / Submitted: subscribe back to the
		// task's event stream and process events normally.
		return a.subscribeAndProcess(ctx, conversationID, toolCallID, taskID, recoveredEmitted, o)
	}
}

// Handles a fresh A2A operation. It sends the AX messages to the agent and
// processes the resulting event stream.
//
// The outgoing message depends on the agent's mode:
//
//   - Stateful (default): only the latest user-input parts are sent.
//     The agent server is expected to reconstruct prior conversation
//     context from contextId via its own session store.
//
//   - Stateless: the full AX history is sent every turn. Necessary
//     for agents that do not maintain their own per-context state.
func (a *A2AAgent) newOperation(
	ctx context.Context,
	conversationID string,
	toolCallID string,
	start *proto.AgentStart,
	o agent.OutputHandler,
) error {
	var parts []*a2a.Part
	if a.stateless {
		parts = a2abridge.MessagesToA2AParts(start.Messages)
	} else {
		parts = a2abridge.LatestUserInputParts(start.Messages)
	}
	if len(parts) == 0 {
		return fmt.Errorf("a2a agent %s: no user-input parts to send", a.id)
	}

	msg := &a2a.Message{
		ID:        a2a.NewMessageID(),
		ContextID: conversationID,
		Role:      a2a.MessageRoleUser,
		Parts:     parts,
	}
	// SendStreamingMessage auto-falls-back to non-streaming SendMessage
	// when the agent does not support streaming. a2abridge.IterateStream
	// consumes both shapes uniformly.
	stream := a.client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	return a2abridge.IterateStream(ctx, a.client, a.id, conversationID, toolCallID, stream, o)
}

// sendFollowUp sends the user's reply (yes/no) to a paused HITL task and
// processes the subsequent event stream.
func (a *A2AAgent) sendFollowUp(
	ctx context.Context,
	conversationID string,
	toolCallID string,
	taskID a2a.TaskID,
	approved bool,
	o agent.OutputHandler,
) error {
	answer := "yes"
	if !approved {
		answer = "no"
	}
	msg := &a2a.Message{
		ID:        a2a.NewMessageID(),
		ContextID: conversationID,
		TaskID:    taskID,
		Role:      a2a.MessageRoleUser,
		Parts:     []*a2a.Part{a2a.NewTextPart(answer)},
	}
	stream := a.client.SendStreamingMessage(ctx, &a2a.SendMessageRequest{Message: msg})
	return a2abridge.IterateStream(ctx, a.client, a.id, conversationID, toolCallID, stream, o)
}

// subscribeAndProcess re-subscribes to a task's event stream (for resuming a
// task that was still working when the bridge previously disconnected).
//
// toolCallID is passed through so that any HITL emitted via the resumed
// stream uses the planner's ToolCallID for ConfirmationContentID.
//
// alreadyEmitted is forwarded to a2abridge.PollUntilTerminal for dedup
// seeding when we fall back to polling.
func (a *A2AAgent) subscribeAndProcess(
	ctx context.Context,
	conversationID string,
	toolCallID string,
	taskID a2a.TaskID,
	alreadyEmitted []string,
	o agent.OutputHandler,
) error {
	if a.card != nil && a.card.Capabilities.Streaming {
		stream := a.client.SubscribeToTask(ctx, &a2a.SubscribeToTaskRequest{ID: taskID})
		return a2abridge.IterateStream(ctx, a.client, a.id, conversationID, toolCallID, stream, o)
	}
	return a2abridge.PollUntilTerminal(ctx, a.client, a.id, conversationID, toolCallID, taskID, nil, alreadyEmitted, o)
}

// HealthCheck performs a lightweight call to verify the agent is reachable.
// Uses GetExtendedAgentCard which is supported by all A2A-compliant agents.
func (a *A2AAgent) HealthCheck(ctx context.Context) error {
	_, err := a.client.GetExtendedAgentCard(ctx, &a2a.GetExtendedAgentCardRequest{})
	return err
}

// Close releases the underlying A2A client's resources.
func (a *A2AAgent) Close() error {
	if a.client == nil {
		return nil
	}
	return a.client.Destroy()
}
