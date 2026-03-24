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
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/ax/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSQLiteEventLog_AppendAndEvents(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	log, err := OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite event log: %v", err)
	}
	defer log.Close()

	ev1 := &proto.ExecutionEvent{
		ExecId:    "task-1",
		State:     proto.State_STATE_PENDING,
		Timestamp: timestamppb.Now(),
		Inputs: []*proto.Content{
			{Role: "user", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hello"}}},
		},
	}

	ev2 := &proto.ExecutionEvent{
		ExecId:    "task-1",
		State:     proto.State_STATE_COMPLETED,
		Timestamp: timestamppb.Now(),
		Outputs: []*proto.Content{
			{Role: "assistant", Content: &proto.Content_Text{Text: &proto.TextContent{Text: "world"}}},
		},
	}

	if err := log.Append(ctx, ev1); err != nil {
		t.Fatalf("failed to append ev1: %v", err)
	}
	if err := log.Append(ctx, ev2); err != nil {
		t.Fatalf("failed to append ev2: %v", err)
	}

	events, err := log.Events(ctx, "task-1")
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}

	if events[0].ExecId != "task-1" || events[0].State != proto.State_STATE_PENDING {
		t.Errorf("ev1 metadata mismatch")
	}
	if events[1].ExecId != "task-1" || events[1].State != proto.State_STATE_COMPLETED {
		t.Errorf("ev2 metadata mismatch")
	}
}

func TestSQLiteEventLog_ConcurrentAppend(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	log, err := OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite event log: %v", err)
	}
	defer log.Close()

	var wg sync.WaitGroup
	numRoutines := 10
	numEvents := 100

	for i := 0; i < numRoutines; i++ {
		wg.Add(1)
		go func(agentIdx int) {
			defer wg.Done()
			for j := 0; j < numEvents; j++ {
				ev := &proto.ExecutionEvent{
					ExecId:    "task-concurrent",
					State:     proto.State(agentIdx % 4), // distribute states 0-3
					Timestamp: timestamppb.Now(),
				}
				if err := log.Append(ctx, ev); err != nil {
					t.Errorf("concurrent append failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	events, err := log.Events(ctx, "task-concurrent")
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	if len(events) != numRoutines*numEvents {
		t.Fatalf("expected %d events, got %d", numRoutines*numEvents, len(events))
	}
}

func TestSQLiteEventLog_Empty(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "test.db")

	log, err := OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite event log: %v", err)
	}
	defer log.Close()

	events, err := log.Events(ctx, "task-1")
	if err != nil {
		t.Fatalf("failed to read events: %v", err)
	}

	if len(events) != 0 {
		t.Fatalf("expected 0 events, got %d", len(events))
	}
}

func TestSQLiteEventLog_CreatesParentDirectory(t *testing.T) {
	// Create a path with a non-existent parent directory
	dbPath := filepath.Join(t.TempDir(), "newdir", "test.db")

	log, err := OpenSQLiteEventLog(dbPath)
	if err != nil {
		t.Fatalf("failed to open sqlite event log and create directory: %v", err)
	}
	defer log.Close()

	// Verify that the parent directory actually exists
	if _, err := os.Stat(filepath.Dir(dbPath)); os.IsNotExist(err) {
		t.Fatalf("expected parent directory to be created, but it does not exist")
	}

	// Verify that the database file was created
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatalf("expected database file to be created, but it does not exist")
	}
}
