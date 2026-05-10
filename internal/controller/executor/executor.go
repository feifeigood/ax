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
	"fmt"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/historyutil"
	"github.com/google/ax/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventLogBuilder func() (EventLog, error)

type defaultExecutor struct {
	execID   string
	eventLog EventLog
	registry map[string]agent.Agent
}

func newExecID(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "-" + child
}

func DefaultExecutor(eventLog EventLog, registry map[string]agent.Agent) agent.Executor {
	return &defaultExecutor{
		execID:   "",
		eventLog: eventLog,
		registry: registry,
	}
}

func (de *defaultExecutor) Exec(ctx context.Context, conversationID string, execID string, start *proto.AgentStart, o agent.OutputHandler) (proto.State, error) {
	execID = newExecID(de.execID, execID)
	a, ok := de.registry[start.AgentId]
	if !ok {
		return proto.State_STATE_UNSPECIFIED, errors.New("no agent found")
	}

	allInputs, state, earlierAgentID, err := history(ctx, de.eventLog, execID)
	if err != nil {
		return proto.State_STATE_UNSPECIFIED, err
	}

	if msg := historyutil.WaitsForConfirmation(allInputs); msg != nil {
		// Ensure that the inputs contain the answer to the
		// confirmation to continue.
		_, conf := historyutil.HasConfirmationAnswer(start.Messages)
		if conf == nil {
			return proto.State_STATE_PENDING, o(&proto.AgentOutputs{
				Messages: []*proto.Message{msg},
			})
		}
	}

	if earlierAgentID != "" && earlierAgentID != start.AgentId {
		return proto.State_STATE_UNSPECIFIED, fmt.Errorf("resumption not allowed: agent ID changed from %s to %s", earlierAgentID, start.AgentId)
	}

	if state == proto.State_STATE_COMPLETED {
		return proto.State_STATE_COMPLETED, nil
	}
	return de.exec(ctx, conversationID, execID, start, de.eventLog, a, allInputs, o)
}

func (de *defaultExecutor) exec(
	ctx context.Context,
	conversationID string,
	execID string,
	start *proto.AgentStart,
	el EventLog,
	a agent.Agent,
	history []*proto.Message,
	o agent.OutputHandler) (proto.State, error) {
	child := &agentExecutor{
		execID:   execID,
		eventLog: de.eventLog,
		registry: de.registry,
	}

	var allOutputs []*proto.Message
	outputBuffer := func(outgoing *proto.AgentOutputs) error {
		allOutputs = append(allOutputs, outgoing.Messages...)
		if o != nil {
			return o(outgoing)
		}
		return nil
	}

	history = append(history, start.Messages...)
	if len(history) == 0 {
		return proto.State_STATE_UNSPECIFIED, errors.New("no inputs")
	}
	if err := logPending(ctx, el, execID, start); err != nil {
		return proto.State_STATE_UNSPECIFIED, err
	}

	start.Messages = history
	if err := a.Connect(ctx, conversationID, execID, start, child, outputBuffer); err != nil {
		// _ = logFailed(ctx, el, execID, start) // Attempt to log failure, but prioritize returning the original error.
		return proto.State_STATE_UNSPECIFIED, err
	}

	if len(allOutputs) > 0 {
		// Log all the outputs at once.
		// TODO: Log only at checkpoints.
		if err := logOutputs(ctx, el, execID, start, allOutputs); err != nil {
			return proto.State_STATE_UNSPECIFIED, err
		}

		last := allOutputs[len(allOutputs)-1]
		if last.GetContent().GetConfirmation() == nil || last.GetContent().GetConfirmation().GetQuestion() == "" {
			// Log completed only if we are not waiting
			// for an answer to a confirmation.
			if err := logCompleted(ctx, el, execID, start); err != nil {
				return proto.State_STATE_UNSPECIFIED, err
			}
			return proto.State_STATE_COMPLETED, nil
		}
	}
	return proto.State_STATE_PENDING, nil
}

func history(ctx context.Context, el EventLog, execID string) ([]*proto.Message, proto.State, string, error) {
	var history []*proto.Message

	var state proto.State
	var agentID string

	if execID != "" {
		execEvents, err := el.ExecEvents(ctx, execID)
		if err != nil {
			return nil, proto.State_STATE_UNSPECIFIED, "", err
		}
		for _, ev := range execEvents {
			if ev.State != proto.State_STATE_UNSPECIFIED {
				state = ev.State
			}
			if ev.AgentId != "" {
				agentID = ev.AgentId
			}
			history = append(history, ev.Inputs...)
			history = append(history, ev.Outputs...)
		}
	}

	return history, state, agentID, nil
}

func logPending(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp:   timestamppb.Now(),
		ExecId:      execID,
		AgentId:     start.AgentId,
		AgentConfig: start.AgentConfig,
		Inputs:      start.Messages,
		State:       proto.State_STATE_PENDING,
	})
}

func logFailed(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		State:     proto.State_STATE_FAILED,
	})
}

func logCompleted(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		State:     proto.State_STATE_COMPLETED,
	})
}

func logOutputs(ctx context.Context, el EventLog, execID string, start *proto.AgentStart, outputs []*proto.Message) error {
	return el.AppendExec(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		Outputs:   outputs,
		State:     proto.State_STATE_PENDING,
	})
}
