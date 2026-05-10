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
	"sync/atomic"
	"testing"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/controller/executor/executortest"
	"github.com/google/ax/proto"
)

func TestAgentExecutor_BasicExec(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	registry := map[string]agent.Agent{
		"echo": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "hello from echo")},
				})
			}
		}),
	}

	ae := AgentExecutor(eventLog, registry)
	var received []*proto.Message
	handler := func(outgoing *proto.AgentOutputs) error {
		received = append(received, outgoing.Messages...)
		return nil
	}

	state, err := ae.Exec(ctx, "conv-1", "exec-1", &proto.AgentStart{
		AgentId:  "echo",
		Messages: []*proto.Message{text("user", "hi")},
	}, handler)
	if err != nil {
		t.Fatal(err)
	}

	if state != proto.State_STATE_COMPLETED {
		t.Fatalf("expected STATE_COMPLETED, got %v", state)
	}
	if len(received) != 1 {
		t.Fatalf("expected 1 output message, got %d", len(received))
	}
	if received[0].GetContent().GetText().GetText() != "hello from echo" {
		t.Fatalf("expected 'hello from echo', got %v", received[0].GetContent().GetText().GetText())
	}

	// Verify event log entries: pending, outputs, completed.
	if len(eventLog.AllExecEvents) != 3 {
		t.Fatalf("expected 3 exec events (pending, outputs, completed), got %d", len(eventLog.AllExecEvents))
	}
	if eventLog.AllExecEvents[0].State != proto.State_STATE_PENDING {
		t.Fatalf("expected first event STATE_PENDING, got %v", eventLog.AllExecEvents[0].State)
	}
	if len(eventLog.AllExecEvents[1].Outputs) != 1 {
		t.Fatalf("expected 1 output in second event, got %d", len(eventLog.AllExecEvents[1].Outputs))
	}
	if eventLog.AllExecEvents[2].State != proto.State_STATE_COMPLETED {
		t.Fatalf("expected last event STATE_COMPLETED, got %v", eventLog.AllExecEvents[2].State)
	}
}

func TestAgentExecutor_ConfirmationReturnsPending(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	confID := "conf-42"

	var callCount atomic.Int32
	registry := map[string]agent.Agent{
		"confirmer": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			callCount.Add(1)
			run := callCount.Load()

			if run == 1 {
				// First invocation: emit a confirmation question.
				if o != nil {
					o(&proto.AgentOutputs{
						Messages: []*proto.Message{{
							Role: "model",
							Content: &proto.Content{
								Type: &proto.Content_Confirmation{
									Confirmation: &proto.ConfirmationContent{
										Id:       confID,
										Question: "proceed?",
									},
								},
							},
						}},
					})
				}
				return
			}

			// Second invocation: emit completion text.
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "confirmed and done")},
				})
			}
		}),
	}

	ae := AgentExecutor(eventLog, registry)

	// --- First call: should return STATE_PENDING ---
	var firstOutputs []*proto.Message
	state, err := ae.Exec(ctx, "conv-1", "exec-conf", &proto.AgentStart{
		AgentId:  "confirmer",
		Messages: []*proto.Message{text("user", "do something risky")},
	}, func(outgoing *proto.AgentOutputs) error {
		firstOutputs = append(firstOutputs, outgoing.Messages...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state != proto.State_STATE_PENDING {
		t.Fatalf("expected STATE_PENDING, got %v", state)
	}
	if len(firstOutputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(firstOutputs))
	}
	if firstOutputs[0].GetContent().GetConfirmation().GetQuestion() != "proceed?" {
		t.Fatalf("expected confirmation question 'proceed?', got %v",
			firstOutputs[0].GetContent().GetConfirmation().GetQuestion())
	}

	// --- Second call: provide approval, should return STATE_COMPLETED ---
	// agentExecutor does NOT check history/cache, so re-invoking with new
	// start.Messages should call the agent again (callCount goes to 2).
	approval := &proto.Message{
		Role: "user",
		Content: &proto.Content{
			Type: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id: confID,
					Decision: &proto.ConfirmationContent_Approval{
						Approval: &proto.ApprovalDecision{Approved: true},
					},
				},
			},
		},
	}

	var secondOutputs []*proto.Message
	state, err = ae.Exec(ctx, "conv-1", "exec-conf", &proto.AgentStart{
		AgentId:  "confirmer",
		Messages: []*proto.Message{approval},
	}, func(outgoing *proto.AgentOutputs) error {
		secondOutputs = append(secondOutputs, outgoing.Messages...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if state != proto.State_STATE_COMPLETED {
		t.Fatalf("expected STATE_COMPLETED on second call, got %v", state)
	}
	if callCount.Load() != 2 {
		t.Fatalf("expected agent called 2 times, got %d", callCount.Load())
	}
	if len(secondOutputs) != 1 {
		t.Fatalf("expected 1 output on second call, got %d", len(secondOutputs))
	}
	if secondOutputs[0].GetContent().GetText().GetText() != "confirmed and done" {
		t.Fatalf("expected 'confirmed and done', got %v",
			secondOutputs[0].GetContent().GetText().GetText())
	}
}

func TestAgentExecutor_NoHistoryLoading(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	execID := "exec-completed"
	agentID := "worker"

	// Pre-populate event log with a COMPLETED execution for the same execID.
	// defaultExecutor would short-circuit at executor.go:78 and return
	// STATE_COMPLETED without calling the agent. agentExecutor must NOT
	// do this — it should always invoke the agent.
	eventLog.AllExecEvents = []*proto.ExecutionEvent{
		{
			ExecId:  execID,
			AgentId: agentID,
			State:   proto.State_STATE_PENDING,
			Inputs:  []*proto.Message{text("user", "old input")},
		},
		{
			ExecId:  execID,
			AgentId: agentID,
			Outputs: []*proto.Message{text("assistant", "old output")},
			State:   proto.State_STATE_PENDING,
		},
		{
			ExecId:  execID,
			AgentId: agentID,
			State:   proto.State_STATE_COMPLETED,
		},
	}

	var agentCalled atomic.Bool
	registry := map[string]agent.Agent{
		agentID: AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			agentCalled.Store(true)
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "fresh output")},
				})
			}
		}),
	}

	ae := AgentExecutor(eventLog, registry)

	var outputs []*proto.Message
	state, err := ae.Exec(ctx, "conv-1", execID, &proto.AgentStart{
		AgentId:  agentID,
		Messages: []*proto.Message{text("user", "new input")},
	}, func(outgoing *proto.AgentOutputs) error {
		outputs = append(outputs, outgoing.Messages...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if !agentCalled.Load() {
		t.Fatal("agent was NOT called — agentExecutor must not short-circuit on COMPLETED history")
	}
	if state != proto.State_STATE_COMPLETED {
		t.Fatalf("expected STATE_COMPLETED, got %v", state)
	}
	if len(outputs) != 1 {
		t.Fatalf("expected 1 output, got %d", len(outputs))
	}
	if outputs[0].GetContent().GetText().GetText() != "fresh output" {
		t.Fatalf("expected 'fresh output', got %v", outputs[0].GetContent().GetText().GetText())
	}

	// Also verify that defaultExecutor WOULD short-circuit for the same setup,
	// confirming agentExecutor's behavior is intentionally different.
	agentCalled.Store(false)
	de := DefaultExecutor(eventLog, registry)
	deState, err := de.Exec(ctx, "conv-1", execID, &proto.AgentStart{
		AgentId:  agentID,
		Messages: []*proto.Message{text("user", "new input")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if deState != proto.State_STATE_COMPLETED {
		t.Fatalf("defaultExecutor: expected STATE_COMPLETED, got %v", deState)
	}
	if agentCalled.Load() {
		t.Fatal("defaultExecutor should have short-circuited but called the agent")
	}
}

func TestAgentExecutor_AgentNotFound(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	ae := AgentExecutor(eventLog, map[string]agent.Agent{})
	_, err := ae.Exec(ctx, "conv-1", "exec-1", &proto.AgentStart{
		AgentId:  "nonexistent",
		Messages: []*proto.Message{text("user", "hi")},
	}, nil)
	if err == nil {
		t.Fatal("expected error for missing agent, got nil")
	}
	if err.Error() != "no agent found: nonexistent" {
		t.Fatalf("expected 'no agent found: nonexistent', got: %v", err)
	}
}

func TestAgentExecutor_NilHandler(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	registry := map[string]agent.Agent{
		"silent": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "output")},
				})
			}
		}),
	}

	ae := AgentExecutor(eventLog, registry)
	state, err := ae.Exec(ctx, "conv-1", "exec-nil", &proto.AgentStart{
		AgentId:  "silent",
		Messages: []*proto.Message{text("user", "hi")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != proto.State_STATE_COMPLETED {
		t.Fatalf("expected STATE_COMPLETED with nil handler, got %v", state)
	}
}

func TestAgentExecutor_NoOutputs(t *testing.T) {
	ctx := context.Background()
	eventLog := &executortest.MemoryEventLog{}

	registry := map[string]agent.Agent{
		"noop": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			// Agent does nothing, emits no outputs.
		}),
	}

	ae := AgentExecutor(eventLog, registry)
	state, err := ae.Exec(ctx, "conv-1", "exec-noop", &proto.AgentStart{
		AgentId:  "noop",
		Messages: []*proto.Message{text("user", "hi")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if state != proto.State_STATE_COMPLETED {
		t.Fatalf("expected STATE_COMPLETED for no-output agent, got %v", state)
	}

	// Should have pending + completed events (no outputs event).
	if len(eventLog.AllExecEvents) != 2 {
		t.Fatalf("expected 2 exec events (pending, completed), got %d", len(eventLog.AllExecEvents))
	}
}
