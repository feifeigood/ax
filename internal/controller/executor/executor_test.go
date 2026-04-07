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
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/ax/internal/agent"
	"github.com/google/ax/internal/historyutil"
	"github.com/google/ax/proto"
	"golang.org/x/sync/errgroup"
)

// MemoryEventLog is an in-memory EventLog useful for testing and short-lived
// executions. It does not survive process restarts.
type MemoryEventLog struct {
	mu         sync.Mutex
	events     []*proto.ConversationEvent
	execEvents []*proto.ExecutionEvent
}

func (m *MemoryEventLog) Append(_ context.Context, event *proto.ConversationEvent) (int32, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seq := int32(len(m.events) + 1)
	event.Seq = seq
	m.events = append(m.events, event)
	return seq, nil
}

func (m *MemoryEventLog) AppendExec(_ context.Context, event *proto.ExecutionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.execEvents = append(m.execEvents, event)
	return nil
}

func (m *MemoryEventLog) Events(_ context.Context, conversationID string) ([]*proto.ConversationEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*proto.ConversationEvent, 0)
	for _, ev := range m.events {
		if ev.ConversationId == conversationID {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (m *MemoryEventLog) ExecEvents(_ context.Context, execID string) ([]*proto.ExecutionEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*proto.ExecutionEvent, 0)
	for _, ev := range m.execEvents {
		if ev.ExecId == execID {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Drop removes every event for which drop returns true.
// It is provided for testing and crash-simulation purposes.
func (m *MemoryEventLog) Drop(drop func(*proto.ConversationEvent) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	kept := m.events[:0]
	for _, ev := range m.events {
		if !drop(ev) {
			kept = append(kept, ev)
		}
	}
	m.events = kept
}

func (m *MemoryEventLog) Close() error {
	return nil
}

// memoryBuilder returns a new EventLogBuilder that creates a fresh MemoryEventLog per task.
func memoryEventLog() EventLog {
	return &MemoryEventLog{}
}

func Example() {
	ctx := context.Background()

	registry := map[string]agent.Agent{
		"planner": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if _, err := tm.Exec(ctx, "deep-research-task", &proto.AgentStart{
				AgentId:  "deep-research",
				Messages: inputs,
			}, o); err != nil {
				return
			}

			if _, err := tm.Exec(ctx, "pub-med-lookup-task", &proto.AgentStart{
				AgentId:  "pub-med-index",
				Messages: inputs,
			}, o); err != nil {
				return
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if _, err := tm.Exec(ctx, "test", &proto.AgentStart{
		AgentId:  "planner",
		Messages: []*proto.Message{text("user", "Hello, I'd like to research cancer treatment options.")},
	}, nil); err != nil {
		log.Fatal(err)
	}
}

func TestTaskManager(t *testing.T) {
	ctx := context.Background()

	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if _, err := tm.Exec(ctx, "child-task", &proto.AgentStart{
				AgentId:  "child",
				Messages: inputs,
			}, o); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if _, err := tm.Exec(ctx, "root-task", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFanout(t *testing.T) {
	ctx := context.Background()

	var executions atomic.Int32
	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			var g errgroup.Group
			for i := range 50 {
				i := i // Capture loop variable.
				g.Go(func() error {
					_, err := tm.Exec(ctx, fmt.Sprintf("child-%d", i), &proto.AgentStart{
						AgentId:  "child",
						Messages: inputs,
					}, nil)
					return err
				})
			}
			if err := g.Wait(); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			executions.Add(1)
			time.Sleep(100 * time.Millisecond)

			var g errgroup.Group
			for i := range 2 {
				i := i // Capture loop variable.
				g.Go(func() error {
					_, err := tm.Exec(ctx, fmt.Sprintf("child2-%d", i), &proto.AgentStart{
						AgentId:  "child2",
						Messages: inputs,
					}, nil)
					return err
				})
			}
			if err := g.Wait(); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "child done")},
				})
			}
		}),
		"child2": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			executions.Add(1)
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "child2 done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if _, err := tm.Exec(ctx, "root-task", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if got, want := executions.Load(), int32(150); got != want {
		t.Fatalf("executions got %v; want %v", got, want)
	}
}

func TestConfirmation(t *testing.T) {
	ctx := context.Background()

	var runCount int
	eventLog := memoryEventLog()

	confID := "test-conf-id"
	var childDone atomic.Bool
	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if _, err := tm.Exec(ctx, "child-task", &proto.AgentStart{
				AgentId:  "child",
				Messages: inputs,
			}, o); err != nil {
				t.Fatal(err)
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if runCount == 0 {
				runCount++
				log.Println("Asking for the question...")
				if o != nil {
					o(&proto.AgentOutputs{
						Messages: []*proto.Message{{
							Role: "model",
							Content: &proto.Content{
								Content: &proto.Content_Confirmation{
									Confirmation: &proto.ConfirmationContent{Id: confID, Question: "proceed?"},
								},
							},
						}},
					})
				}
				return
			}

			lastInput := inputs[len(inputs)-1]
			if lastInput.GetContent().GetConfirmation() == nil || lastInput.GetContent().GetConfirmation().GetDecision() == nil {
				t.Fatal("no decision in the incoming inputs")
			}

			childDone.Store(true)
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(eventLog, registry)

	// First run: child returns a confirmation request.
	if _, err := tm.Exec(ctx, "root-task", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Re-run with the approval decision as new input.
	approval := &proto.Message{
		Role: "user",
		Content: &proto.Content{
			Content: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id: confID,
					Decision: &proto.ConfirmationContent_Approval{
						Approval: &proto.ApprovalDecision{Approved: true},
					},
				},
			},
		},
	}
	if _, err := tm.Exec(ctx, "root-task", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{approval},
	}, nil); err != nil {
		t.Fatal(err)
	}

	if !childDone.Load() {
		t.Fatal("child is not done")
	}
}

func TestResume(t *testing.T) {
	ctx := context.Background()
	eventLog := memoryEventLog()

	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if _, err := tm.Exec(ctx, "child-task", &proto.AgentStart{
				AgentId:  "child",
				Messages: inputs,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(eventLog, registry)
	if _, err := tm.Exec(ctx, "root-task", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestResumeAgentIDMismatch(t *testing.T) {
	ctx := context.Background()
	eventLog := memoryEventLog()

	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			// Do nothing to leave it in PENDING state
		}),
		"other": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
		}),
	}

	tm := DefaultExecutor(eventLog, registry)

	// First run: starts as "root"
	if _, err := tm.Exec(ctx, "task1", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Second run: attempts to resume as "other" for same execID "task1"
	_, err := tm.Exec(ctx, "task1", &proto.AgentStart{
		AgentId:  "other",
		Messages: []*proto.Message{text("user", "hello again!")},
	}, nil)

	if err == nil {
		t.Fatal("expected error due to agent ID mismatch, got nil")
	}

	if !strings.Contains(err.Error(), "resumption not allowed") {
		t.Fatalf("expected 'resumption not allowed' error, got: %v", err)
	}
}

func TestResumeConfirmation(t *testing.T) {
	ctx := context.Background()
	eventLog := memoryEventLog()

	msg := &proto.Message{
		Content: &proto.Content{
			Content: &proto.Content_Confirmation{
				Confirmation: &proto.ConfirmationContent{
					Id:       "test-conf-id",
					Question: "proceed?",
				},
			},
		},
	}

	var runCount int
	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Message, tm agent.Executor, o agent.OutputHandler) {
			if conf := historyutil.WaitsForConfirmation(inputs); conf != nil {
				o(&proto.AgentOutputs{
					Messages: []*proto.Message{msg},
				})
				return
			}

			approved, _ := historyutil.HasConfirmationAnswer(inputs)
			if approved {
				if err := o(&proto.AgentOutputs{
					// "Hello!" is responded with a confirmation request.
					Messages: []*proto.Message{{
						Content: &proto.Content{
							Content: &proto.Content_Text{
								Text: &proto.TextContent{
									Text: "awesome!",
								},
							},
						},
					}},
				}); err != nil {
					t.Fatal(err)
				}
			}

			if runCount == 0 {
				runCount++
				if err := o(&proto.AgentOutputs{
					// "Hello!" is responded with a confirmation request.
					Messages: []*proto.Message{msg},
				}); err != nil {
					t.Fatal(err)
				}
			}
		}),
	}

	tm := DefaultExecutor(eventLog, registry)

	// First run: starts as "root"
	if _, err := tm.Exec(ctx, "task1", &proto.AgentStart{
		AgentId:  "root",
		Messages: []*proto.Message{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var confirmation *proto.ConfirmationContent
	handler := func(outputs *proto.AgentOutputs) error {
		confirmation = outputs.Messages[0].GetContent().GetConfirmation()
		return nil
	}
	// Second run: attempts to resume as "other" for same execID "task1"
	if _, err := tm.Exec(ctx, "task1", &proto.AgentStart{
		AgentId: "root",
	}, handler); err != nil {
		t.Fatal(err)
	}
	if confirmation == nil {
		t.Fatal("confirmation is nil")
	}
	if confirmation.Id != "test-conf-id" {
		t.Fatalf("confirmation id mismatch: got %v, want %v", confirmation.Id, "test-conf-id")
	}

	// Third run: user provides an approval decision.
	var msgs []*proto.Message
	handler = func(outputs *proto.AgentOutputs) error {
		msgs = outputs.Messages
		return nil
	}
	if _, err := tm.Exec(ctx, "task1", &proto.AgentStart{
		AgentId: "root",
		Messages: []*proto.Message{
			{
				Content: &proto.Content{
					Content: &proto.Content_Confirmation{
						Confirmation: &proto.ConfirmationContent{
							Id: "test-conf-id",
							Decision: &proto.ConfirmationContent_Approval{
								Approval: &proto.ApprovalDecision{
									Approved: true,
								},
							},
						},
					},
				},
			},
		}}, handler); err != nil {
		t.Fatal(err)
	}

	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", len(msgs))
	}
	if msgs[0].GetContent().GetText().GetText() != "awesome!" {
		t.Fatalf("expected 'awesome!', got %v", msgs[0].GetContent().GetText().GetText())
	}
}
