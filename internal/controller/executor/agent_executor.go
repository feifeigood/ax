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

package executor

import (
	"context"
	"errors"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/historyutil"
	"github.com/google/ax/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// AgentExecutor returns an Executor suitable for invoking sub-agents from
// within a planner or another agent's Connect method.
//
// Unlike DefaultExecutor (which is the controller's top-level conversation
// runner), AgentExecutor:
//   - Does NOT load conversation history from the event log; the caller
//     passes the relevant messages in start.Messages.
//   - Does NOT short-circuit on WaitsForConfirmation or COMPLETED cache.
//   - DOES write execution-level logs (pending, outputs, completed) for
//     observability, but omits the WaitsForConfirmation/COMPLETED guards
//     that DefaultExecutor uses.
//   - Returns STATE_PENDING when the sub-agent's last output is an
//     unanswered confirmation question.
func AgentExecutor(eventLog EventLog, registry map[string]agent.Agent) agent.Executor {
	return &agentExecutor{
		eventLog: eventLog,
		registry: registry,
	}
}

type agentExecutor struct {
	execID   string
	eventLog EventLog
	registry map[string]agent.Agent
}

func (ae *agentExecutor) Exec(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, o agent.OutputHandler) (proto.State, error) {
	execID = newExecID(ae.execID, execID)
	a, ok := ae.registry[start.AgentId]
	if !ok {
		return proto.State_STATE_UNSPECIFIED, errors.New("no agent found: " + start.AgentId)
	}

	// Log pending execution event with inputs for debuggability.
	if err := ae.eventLog.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		Inputs:    start.Messages,
		State:     proto.State_STATE_PENDING,
	}); err != nil {
		return proto.State_STATE_UNSPECIFIED, err
	}

	var allOutputs []*proto.Message
	outputBuffer := func(outgoing *proto.AgentOutputs) error {
		allOutputs = append(allOutputs, outgoing.Messages...)
		if o != nil {
			return o(outgoing)
		}
		return nil
	}

	if err := a.Connect(ctx, conversationID, execID, start, &agentExecutor{execID: execID, eventLog: ae.eventLog, registry: ae.registry}, outputBuffer); err != nil {
		return proto.State_STATE_UNSPECIFIED, err
	}

	// Log outputs.
	if len(allOutputs) > 0 {
		if err := ae.eventLog.AppendExec(ctx, &proto.ExecutionEvent{
			Timestamp: timestamppb.Now(),
			ExecId:    execID,
			AgentId:   start.AgentId,
			Outputs:   allOutputs,
			State:     proto.State_STATE_PENDING,
		}); err != nil {
			return proto.State_STATE_UNSPECIFIED, err
		}

		if historyutil.WaitsForConfirmation(allOutputs) != nil {
			return proto.State_STATE_PENDING, nil
		}
	}

	// Log completed.
	if err := ae.eventLog.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		State:     proto.State_STATE_COMPLETED,
	}); err != nil {
		return proto.State_STATE_UNSPECIFIED, err
	}
	return proto.State_STATE_COMPLETED, nil
}
