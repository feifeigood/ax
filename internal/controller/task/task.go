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

package task

import (
	"context"
	"errors"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type EventLogBuilder func() (EventLog, error)

type taskExecutor struct {
	id       string
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
	return &taskExecutor{
		id:       "",
		eventLog: eventLog,
		registry: registry,
	}
}

func (tm *taskExecutor) Exec(ctx context.Context, execID string, start *proto.AgentStart, o agent.OutputHandler) error {
	execID = newExecID(tm.id, execID)
	a, ok := tm.registry[start.AgentId]
	if !ok {
		return errors.New("no agent found")
	}

	allInputs, state, err := history(ctx, tm.eventLog, execID)
	if err != nil {
		return err
	}

	if state == proto.State_STATE_COMPLETED {
		return nil
	}
	return tm.exec(ctx, execID, start, tm.eventLog, a, allInputs, o)
}

func (tm *taskExecutor) exec(
	ctx context.Context,
	execID string,
	start *proto.AgentStart,
	el EventLog,
	a agent.Agent,
	allInputs []*proto.Content,
	o agent.OutputHandler) error {
	child := &taskExecutor{
		id:       execID,
		eventLog: tm.eventLog,
		registry: tm.registry,
	}

	var allOutputs []*proto.Content
	outputBuffer := func(outgoing *proto.AgentOutputs) error {
		allOutputs = append(allOutputs, outgoing.Contents...)
		if o != nil {
			return o(outgoing)
		}
		return nil
	}

	allInputs = append(allInputs, start.Contents...)
	if len(allInputs) == 0 {
		return errors.New("no inputs")
	}
	if err := logPending(ctx, el, execID, start); err != nil {
		return err
	}

	start.Contents = allInputs
	if err := a.Connect(ctx, execID, start, child, outputBuffer); err != nil {
		_ = logFailed(ctx, el, execID, start) // Attempt to log failure, but prioritize returning the original error.
		return err
	}

	if len(allOutputs) > 0 {
		// Log all the outputs at once.
		// TODO: Log only at checkpoints.
		if err := logOutputs(ctx, el, execID, start, allOutputs); err != nil {
			return err
		}

		last := allOutputs[len(allOutputs)-1]
		if last.GetConfirmation() == nil || last.GetConfirmation().GetQuestion() == "" {
			// Log completed only if we are not waiting
			// for an answer to a confirmation.
			return logCompleted(ctx, el, execID, start)
		}
	}
	return nil
}

func history(ctx context.Context, el EventLog, execID string) ([]*proto.Content, proto.State, error) {
	events, err := el.Events(ctx, execID)
	if err != nil {
		return nil, proto.State_STATE_UNSPECIFIED, err
	}

	var history []*proto.Content
	var state proto.State

	for _, event := range events {
		if event.ExecId != execID {
			continue
		}
		// Reset after the status change ensure
		// that we have a clean state even if we are
		// presented an event log with dirty entries
		// from previous runs.
		if event.State == proto.State_STATE_PENDING {
			history = []*proto.Content{}
		}
		history = append(history, event.Inputs...)
		history = append(history, event.Outputs...)
		state = event.State
	}

	if state == proto.State_STATE_COMPLETED || state == proto.State_STATE_FAILED {
		return history, state, nil
	}

	return history, state, nil
}

func logPending(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		Inputs:    start.Contents,
		State:     proto.State_STATE_PENDING,
	})
}

func logFailed(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		State:     proto.State_STATE_FAILED,
	})
}

func logCompleted(ctx context.Context, el EventLog, execID string, start *proto.AgentStart) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		State:     proto.State_STATE_COMPLETED,
	})
}

func logOutputs(ctx context.Context, el EventLog, execID string, start *proto.AgentStart, outputs []*proto.Content) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		ExecId:    execID,
		AgentId:   start.AgentId,
		Outputs:   outputs,
	})
}
