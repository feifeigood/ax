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
)

const plannerAgentID = "__planner"

// LoopExecutor orchestrates the agentic loop workflow.
// It implements the plan-execute-evaluate-checkpoint cycle.
type LoopExecutor struct {
	registry       *Registry
	sessionManager *SessionManager
	maxSteps       int
	planner        agent.Agent
}

// LoopConfig configures the loop executor.
type LoopConfig struct {
	Registry       *Registry
	SessionManager *SessionManager
	MaxSteps       int
	Planner        agent.Agent
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
		return nil, fmt.Errorf("max_steps cannot be zero")
	}
	// Plan function is required
	if config.Planner == nil {
		return nil, fmt.Errorf("planner is required")
	}

	return &LoopExecutor{
		registry:       config.Registry,
		sessionManager: config.SessionManager,
		maxSteps:       config.MaxSteps,
		planner:        config.Planner,
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

		return fmt.Errorf("resuming waiting agents is not yet supported")
	}

	// Ensure the last confirmation question was answered.
	exit, nextAgentID, err := e.handleConfirmation(ctx, session, incoming, handler)
	if err != nil {
		return err
	}
	if exit {
		return nil
	}
	if nextAgentID == "" {
		nextAgentID = plannerAgentID
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

		history := session.History()
		handoff, err := e.runAgent(ctx, session, nextAgentID, history, handler)
		if err != nil {
			return err
		}

		if handoff == "" {
			// No more agent handoffs to execute; loop is complete.
			return nil
		}

		nextAgentID = handoff
		steps++
	}

	// Can be resumed later with another trigger
	return fmt.Errorf("max steps per trigger (%d) reached", e.maxSteps)
}

func (e *LoopExecutor) runAgent(ctx context.Context, session *Session, agentID string, inputs []*proto.Content, handler agent.OutputHandler) (handoff string, err error) {
	var buffer []*proto.Content

	// Helper to flush buffer to session
	flushBuffer := func(checkpointID string) error {
		if len(buffer) == 0 {
			return nil
		}
		if err := session.WriteContent(ctx, agentID, checkpointID, buffer); err != nil {
			return fmt.Errorf("failed to write content: %w", err)
		}
		buffer = []*proto.Content{} // Clear buffer after successful write
		return nil
	}

	runHandler := func(outgoing *proto.ProcessResponse) error {
		buffer = append(buffer, outgoing.Contents...)

		if outgoing.CheckpointId != "" {
			if err := flushBuffer(outgoing.CheckpointId); err != nil {
				return err
			}
		}

		if outgoing.AgentHandoff != "" {
			// Write any pending content before handoff
			if err := flushBuffer(""); err != nil {
				return err
			}

			handoff = outgoing.AgentHandoff
			if err := session.WriteAgentHandoff(ctx, agentID, handoff); err != nil {
				return fmt.Errorf("failed to write handoff: %w", err)
			}
		}
		return handler(outgoing)
	}

	var a agent.Agent
	if agentID == plannerAgentID {
		a = e.planner
	} else {
		a, err = e.registry.Get(agentID)
		if err != nil {
			return "", fmt.Errorf("failed to get agent: %w", err)
		}
	}

	if err := a.Process(ctx, session.ID(), &proto.ProcessRequest{
		Contents: inputs,
	}, runHandler); err != nil {
		return "", fmt.Errorf("agent process failed: %w", err)
	}

	// Final flush of any remaining buffer content
	if err := flushBuffer(""); err != nil {
		return "", err
	}
	return handoff, nil
}

// handleConfirmation checks if the session is waiting for a confirmation response,
// and if so, verifies that the incoming request provides a matching response.
// It returns a boolean indicating whether the loop should stop early, the agent to resume from, and an error.
func (e *LoopExecutor) handleConfirmation(ctx context.Context, session *Session, incoming *proto.ProcessRequest, handler agent.OutputHandler) (exitLoop bool, resumedAgentID string, err error) {
	history := session.History()
	if len(history) == 0 {
		return false, "", nil
	}
	lastMsg := history[len(history)-1]
	confReq := lastMsg.GetConfirmation()
	if confReq == nil {
		// not a confirmation question or answer
		return false, "", nil
	}

	if lastMsg.Role != "assistant" || confReq.Id == "" || confReq.Question == "" {
		// not a confirmation question
		return false, "", nil
	}

	// Require the first incoming content to be the confirmation response
	if len(incoming.Contents) == 0 {
		return true, "", handler(&proto.ProcessResponse{
			Contents: []*proto.Content{lastMsg},
		})
	}

	confResp := incoming.Contents[0].GetConfirmation()
	if confResp == nil || (confResp.GetApproval() == nil && confResp.GetDecline() == nil) {
		return true, "", handler(&proto.ProcessResponse{
			Contents: []*proto.Content{lastMsg},
		})
	}

	if confResp.Id != confReq.Id {
		return true, "", handler(&proto.ProcessResponse{
			Contents: []*proto.Content{lastMsg},
		})
	}
	if confResp.GetDecline() != nil {
		out := []*proto.Content{{
			Role: "assistant",
			Content: &proto.Content_Text{
				// Respond with "Ok" instead of the rejection decision
				// to ensure the loop isn't stuck on that rejection decision.
				// Models keep rejecting the confirmation in the lifetime
				// of a session otherwise. Given we are explicitly asking
				// for confirmation, this is safe.
				Text: &proto.TextContent{Text: "Ok."},
			},
		}}

		if err := session.WriteContent(ctx, plannerAgentID, "", out); err != nil {
			return true, "", fmt.Errorf("failed to write content: %w", err)
		}
		return true, "", handler(&proto.ProcessResponse{Contents: out})
	}
	return false, session.ConfirmationWaitingAgent(confResp.Id), nil
}
