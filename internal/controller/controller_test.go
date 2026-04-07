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
	"sync"
	"testing"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/controller/executor"
	"github.com/google/ax/proto"
)

type mockEventLog struct {
	mu         sync.Mutex
	events     []*proto.ConversationEvent
	execEvents []*proto.ExecutionEvent
}

func (m *mockEventLog) Append(ctx context.Context, event *proto.ConversationEvent) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	seq := int32(len(m.events) + 1)
	event.Seq = seq
	m.events = append(m.events, event)
	return seq, nil
}

func (m *mockEventLog) AppendExec(ctx context.Context, event *proto.ExecutionEvent) error {
	m.execEvents = append(m.execEvents, event)
	return nil
}

func (m *mockEventLog) Events(ctx context.Context, conversationID string) ([]*proto.ConversationEvent, error) {
	var out []*proto.ConversationEvent
	for _, ev := range m.events {
		if ev.ConversationId == conversationID {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *mockEventLog) ExecEvents(ctx context.Context, execID string) ([]*proto.ExecutionEvent, error) {
	var out []*proto.ExecutionEvent
	for _, ev := range m.execEvents {
		if ev.ExecId == execID {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *mockEventLog) Close() error {
	return nil
}

type dummyAgent struct{}

func (a *dummyAgent) Connect(ctx context.Context, execID string, start *proto.AgentStart, e agent.Executor, o agent.OutputHandler) error {
	return nil
}
func (a *dummyAgent) HealthCheck(ctx context.Context) error { return nil }
func (a *dummyAgent) Close() error                          { return nil }

func TestController_Exec_ResumptionAndIDGeneration(t *testing.T) {
	ctx := context.Background()
	cid := "test-conv"

	inputs := []*proto.Message{
		{
			Role: "user",
			Content: &proto.Content{
				Content: &proto.Content_Text{
					Text: &proto.TextContent{Text: "hello"},
				},
			},
		},
	}

	// Case 1: No history, new inputs. Should create a new execution with a UUID.
	log := &mockEventLog{}
	c, err := New(ctx, Config{
		EventLogBuilder: func() (executor.EventLog, error) {
			return log, nil
		},
		PlannerBuilder: func(ctx context.Context, r *Registry) (agent.Agent, error) {
			return &dummyAgent{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.Exec(ctx, &proto.ExecRequest{
		ConversationId: cid,
		Inputs:         inputs,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(log.events) == 0 {
		t.Fatal("expected events to be logged")
	}
	execID := log.events[0].ExecId
	if execID == "" || execID == cid {
		t.Fatalf("expected a new random execID, got %v", execID)
	}

	// Case 2: History exists, PENDING state, inputs empty. Should replay/resume.
	// Modify the event logged by logPending in Case 1 to use dummy-agent.
	log.events[len(log.events)-1].State = proto.State_STATE_PENDING

	err = c.Exec(ctx, &proto.ExecRequest{
		ConversationId: cid,
		Inputs:         []*proto.Message{},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Replay should have called `e.Exec` for that `execID`.
	lastEventID := log.events[len(log.events)-1].ExecId
	if lastEventID != execID {
		t.Fatalf("expected resumed execution ID %v, got %v", execID, lastEventID)
	}

	// Case 3: History exists, COMPLETED state, new inputs. Should create a NEW execution.
	for _, ev := range log.execEvents {
		if ev.ExecId == execID {
			ev.State = proto.State_STATE_COMPLETED
		}
	}
	// Also populate messages in conversation log to simulate completion.
	log.events[len(log.events)-1].Messages = []*proto.Message{
		{Role: "user", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hello"}}}},
	}
	log.events[len(log.events)-1].State = proto.State_STATE_COMPLETED

	err = c.Exec(ctx, &proto.ExecRequest{
		ConversationId: cid,
		Inputs:         inputs,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	newExecID := log.events[len(log.events)-1].ExecId
	if newExecID == execID {
		t.Fatal("expected a NEW execution ID, but it was reused")
	}
}


func TestController_Exec_LastSeenSeq_Empty(t *testing.T) {
	ctx := context.Background()
	cid := "test-conv-seq"

	log := &mockEventLog{}
	// Pre-populate history
	log.events = []*proto.ConversationEvent{
		{
			ConversationId: cid,
			Seq:            1,
			Messages: []*proto.Message{
				{Role: "user", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "msg 1"}}}},
			},
			State: proto.State_STATE_COMPLETED,
		},
		{
			ConversationId: cid,
			Seq:            2,
			Messages: []*proto.Message{
				{Role: "assistant", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "msg 2"}}}},
			},
			State: proto.State_STATE_COMPLETED,
		},
	}

	c, err := New(ctx, Config{
		EventLogBuilder: func() (executor.EventLog, error) {
			return log, nil
		},
		PlannerBuilder: func(ctx context.Context, r *Registry) (agent.Agent, error) {
			return &dummyAgent{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var msgs []*proto.Message
	handler := ExecHandler(func(resp *proto.ExecResponse) error {
		msgs = append(msgs, resp.Outputs...)
		return nil
	})

	err = c.Exec(ctx, &proto.ExecRequest{
		ConversationId: cid,
	}, handler)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected 0 messages, got %d", len(msgs))
	}
}

func TestController_Exec_LastSeenSeq(t *testing.T) {
	ctx := context.Background()
	cid := "test-conv-seq"

	log := &mockEventLog{}
	// Pre-populate history
	log.events = []*proto.ConversationEvent{
		{
			ConversationId: cid,
			Seq:            1,
			Messages: []*proto.Message{
				{Role: "user", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "msg 1"}}}},
			},
			State: proto.State_STATE_COMPLETED,
		},
		{
			ConversationId: cid,
			Seq:            2,
			Messages: []*proto.Message{
				{Role: "assistant", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "msg 2"}}}},
			},
			State: proto.State_STATE_COMPLETED,
		},
	}

	c, err := New(ctx, Config{
		EventLogBuilder: func() (executor.EventLog, error) {
			return log, nil
		},
		PlannerBuilder: func(ctx context.Context, r *Registry) (agent.Agent, error) {
			return &dummyAgent{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var msgs []*proto.Message
	handler := ExecHandler(func(resp *proto.ExecResponse) error {
		msgs = append(msgs, resp.Outputs...)
		return nil
	})

	err = c.Exec(ctx, &proto.ExecRequest{
		ConversationId: cid,
		LastSeenSeq:    1,
	}, handler)
	if err != nil {
		t.Fatal(err)
	}

	// We expect to receive messages from Seq 2 (since LastSeenSeq is 1).
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].GetContent().GetText().GetText() != "msg 2" {
		t.Fatalf("expected 'msg 2', got %v", msgs[0].GetContent().GetText().GetText())
	}
}
