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
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/ax/agent"
	"github.com/google/ax/proto"
	"golang.org/x/sync/errgroup"
)

// MemoryEventLog is an in-memory EventLog useful for testing and short-lived
// executions. It does not survive process restarts.
type MemoryEventLog struct {
	mu     sync.Mutex
	events []*proto.ExecutionEvent
}

func (m *MemoryEventLog) Append(_ context.Context, event *proto.ExecutionEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.events = append(m.events, event)
	return nil
}

func (m *MemoryEventLog) Events(_ context.Context, taskID string) ([]*proto.ExecutionEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]*proto.ExecutionEvent, 0)
	for _, ev := range m.events {
		if ev.TaskId == taskID {
			out = append(out, ev)
		}
	}
	return out, nil
}

// Drop removes every event for which drop returns true.
// It is provided for testing and crash-simulation purposes.
func (m *MemoryEventLog) Drop(drop func(*proto.ExecutionEvent) bool) {
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
		"planner": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			if err := tm.Exec(ctx, &agent.Task{
				ID:      "deep-research-task",
				AgentID: "deep-research",
				Inputs:  inputs,
			}, o); err != nil {
				return
			}

			if err := tm.Exec(ctx, &agent.Task{
				ID:      "pub-med-lookup-task",
				AgentID: "pub-med-index",
				Inputs:  inputs,
			}, o); err != nil {
				return
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "test",
		AgentID: "planner",
		Inputs:  []*proto.Content{text("user", "Hello, I'd like to research cancer treatment options.")},
	}, nil); err != nil {
		log.Fatal(err)
	}
}

func TestTaskManager(t *testing.T) {
	ctx := context.Background()

	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			if err := tm.Exec(ctx, &agent.Task{
				ID:      "child-task",
				AgentID: "child",
				Inputs:  inputs,
			}, o); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "root-task",
		AgentID: "root",
		Inputs:  []*proto.Content{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestFanout(t *testing.T) {
	ctx := context.Background()

	var executions atomic.Int32
	registry := map[string]agent.Agent{
		"root": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			var g errgroup.Group
			for i := 0; i < 50; i++ {
				i := i // Capture loop variable.
				g.Go(func() error {
					return tm.Exec(ctx, &agent.Task{
						ID:      fmt.Sprintf("child-%d", i),
						AgentID: "child",
						Inputs:  inputs,
					}, nil)
				})
			}
			if err := g.Wait(); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			executions.Add(1)
			time.Sleep(100 * time.Millisecond)

			var g errgroup.Group
			for i := 0; i < 2; i++ {
				i := i // Capture loop variable.
				g.Go(func() error {
					return tm.Exec(ctx, &agent.Task{
						ID:      fmt.Sprintf("child2-%d", i),
						AgentID: "child2",
						Inputs:  inputs,
					}, nil)
				})
			}
			if err := g.Wait(); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "child done")},
				})
			}
		}),
		"child2": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			executions.Add(1)
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "child2 done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(memoryEventLog(), registry)
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "root-task",
		AgentID: "root",
		Inputs:  []*proto.Content{text("user", "hello!")},
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
		"root": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			if err := tm.Exec(ctx, &agent.Task{
				ID:      "child-task",
				AgentID: "child",
				Inputs:  inputs,
			}, o); err != nil {
				t.Fatal(err)
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			if runCount == 0 {
				runCount++
				log.Println("Asking for the question...")
				if o != nil {
					o(&proto.ProcessResponse{
						Contents: []*proto.Content{{
							Content: &proto.Content_Confirmation{
								Confirmation: &proto.ConfirmationContent{Id: confID, Question: "proceed?"},
							},
						}},
					})
				}
				return
			}

			lastInput := inputs[len(inputs)-1]
			if lastInput.GetConfirmation() == nil || lastInput.GetConfirmation().GetDecision() == nil {
				t.Fatal("no decision in the incoming inputs")
			}

			childDone.Store(true)
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(eventLog, registry)

	// First run: child returns a confirmation request.
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "root-task",
		AgentID: "root",
		Inputs:  []*proto.Content{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}

	// Re-run with the approval decision as new input.
	approval := &proto.Content{
		Role: "user",
		Content: &proto.Content_Confirmation{
			Confirmation: &proto.ConfirmationContent{
				Id: confID,
				Decision: &proto.ConfirmationContent_Approval{
					Approval: &proto.ApprovalDecision{Approved: true},
				},
			},
		},
	}
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "root-task",
		AgentID: "root",
		Inputs:  []*proto.Content{approval},
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
		"root": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			if err := tm.Exec(ctx, &agent.Task{
				ID:      "child-task",
				AgentID: "child",
				Inputs:  inputs,
			}, nil); err != nil {
				t.Fatal(err)
			}
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "root done")},
				})
			}
		}),
		"child": AgentFunc(func(inputs []*proto.Content, tm agent.TaskExecutor, o agent.OutputHandler) {
			time.Sleep(100 * time.Millisecond)
			if o != nil {
				o(&proto.ProcessResponse{
					Contents: []*proto.Content{text("assistant", "child done")},
				})
			}
		}),
	}

	tm := DefaultExecutor(eventLog, registry)
	if err := tm.Exec(ctx, &agent.Task{
		ID:      "root-task",
		AgentID: "root",
		Inputs:  []*proto.Content{text("user", "hello!")},
	}, nil); err != nil {
		t.Fatal(err)
	}
}
