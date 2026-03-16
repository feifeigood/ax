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
	id        string
	eventLog  EventLog
	registry  map[string]agent.Agent
}

func newTaskID(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "-" + child
}

func DefaultExecutor(eventLog EventLog, registry map[string]agent.Agent) agent.TaskExecutor {
	return &taskExecutor{
		id:        "",
		eventLog:  eventLog,
		registry:  registry,
	}
}

func (tm *taskExecutor) Exec(ctx context.Context, t *agent.Task, o agent.OutputHandler) error {
	t.ID = newTaskID(tm.id, t.ID)
	a, ok := tm.registry[t.AgentID]
	if !ok {
		return errors.New("no agent found")
	}

	allInputs, state, err := history(ctx, tm.eventLog, t.ID)
	if err != nil {
		return err
	}

	if state == proto.State_STATE_COMPLETED {
		if t.Rehydrate {
			return o(&proto.ProcessResponse{
				Contents:  allInputs,
				FromCache: true,
			})
		}
		return nil
	}
	return tm.exec(ctx, t, tm.eventLog, a, allInputs, o)
}

func (tm *taskExecutor) exec(
	ctx context.Context,
	t *agent.Task,
	el EventLog,
	a agent.Agent,
	allInputs []*proto.Content,
	o agent.OutputHandler) error {
	child := &taskExecutor{
		id:        t.ID,
		eventLog:  tm.eventLog,
		registry:  tm.registry,
	}

	var allOutputs []*proto.Content
	outputBuffer := func(outgoing *proto.ProcessResponse) error {
		allOutputs = append(allOutputs, outgoing.Contents...)
		if o != nil {
			return o(outgoing)
		}
		return nil
	}

	allInputs = append(allInputs, t.Inputs...)
	if err := logPending(ctx, el, t); err != nil {
		return err
	}

	if len(allInputs) == 0 {
		return errors.New("no inputs")
	}

	if err := a.Process(ctx, &agent.Task{
		ID:      t.ID,
		AgentID: t.AgentID,
		Inputs:  allInputs,
	}, child, outputBuffer); err != nil {
		_ = logFailed(ctx, el, t) // Attempt to log failure, but prioritize returning the original error.
		return err
	}

	if len(allOutputs) > 0 {
		// Log all the outputs at once.
		// TODO: Log only at checkpoints.
		if err := logOutputs(ctx, el, t, allOutputs); err != nil {
			return err
		}

		last := allOutputs[len(allOutputs)-1]
		if last.GetConfirmation() == nil || last.GetConfirmation().GetQuestion() == "" {
			// Log completed only if we are not waiting
			// for an answer to a confirmation.
			return logCompleted(ctx, el, t)
		}
	}
	return nil
}

func history(ctx context.Context, el EventLog, taskID string) ([]*proto.Content, proto.State, error) {
	events, err := el.Events(ctx, taskID)
	if err != nil {
		return nil, proto.State_STATE_UNSPECIFIED, err
	}

	var history []*proto.Content
	var state proto.State

	for _, event := range events {
		if event.TaskId != taskID {
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

func logPending(ctx context.Context, el EventLog, t *agent.Task) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		TaskId:    t.ID,
		AgentId:   t.AgentID,
		Inputs:    t.Inputs,
		State:     proto.State_STATE_PENDING,
	})
}

func logFailed(ctx context.Context, el EventLog, t *agent.Task) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		TaskId:    t.ID,
		AgentId:   t.AgentID,
		State:     proto.State_STATE_FAILED,
	})
}

func logCompleted(ctx context.Context, el EventLog, t *agent.Task) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		TaskId:    t.ID,
		AgentId:   t.AgentID,
		State:     proto.State_STATE_COMPLETED,
	})
}

func logOutputs(ctx context.Context, el EventLog, t *agent.Task, outputs []*proto.Content) error {
	return el.Append(ctx, &proto.ExecutionEvent{
		Timestamp: timestamppb.Now(),
		TaskId:    t.ID,
		AgentId:   t.AgentID,
		Outputs:   outputs,
	})
}
