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

	// 1. Test Conversation Log
	cev1 := &proto.ConversationEvent{
		ConversationId: "conv-1",
		Seq:            1,
		ExecId:         "task-1",
	}

	cev2 := &proto.ConversationEvent{
		ConversationId: "conv-1",
		Seq:            2,
		ExecId:         "task-2",
	}

	if err := log.Append(ctx, cev1); err != nil {
		t.Fatalf("failed to append cev1: %v", err)
	}
	if err := log.Append(ctx, cev2); err != nil {
		t.Fatalf("failed to append cev2: %v", err)
	}

	cEvents, err := log.Events(ctx, "conv-1")
	if err != nil {
		t.Fatalf("failed to read conversation events: %v", err)
	}

	if len(cEvents) != 2 {
		t.Fatalf("expected 2 conversation events, got %d", len(cEvents))
	}

	if cEvents[0].ExecId != "task-1" || cEvents[1].ExecId != "task-2" {
		t.Errorf("conversation events mismatch")
	}

	// 2. Test Execution Log
	ee1 := &proto.ExecutionEvent{
		ExecId:    "task-1",
		State:     proto.State_STATE_PENDING,
		Timestamp: timestamppb.Now(),
		Inputs: []*proto.Message{
			{Role: "user", Content: &proto.Content{Content: &proto.Content_Text{Text: &proto.TextContent{Text: "hello"}}}},
		},
	}

	if err := log.AppendExec(ctx, ee1); err != nil {
		t.Fatalf("failed to append ee1: %v", err)
	}

	eEvents, err := log.ExecEvents(ctx, "task-1")
	if err != nil {
		t.Fatalf("failed to read execution events: %v", err)
	}

	if len(eEvents) != 1 {
		t.Fatalf("expected 1 execution event, got %d", len(eEvents))
	}

	if eEvents[0].ExecId != "task-1" || eEvents[0].State != proto.State_STATE_PENDING {
		t.Errorf("execution event mismatch")
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

	for i := range numRoutines {
		wg.Add(1)
		go func(agentIdx int) {
			defer wg.Done()
			for i := range numEvents {
				ev := &proto.ConversationEvent{
					ConversationId: "conv-concurrent",
					Seq:            int32(agentIdx*numEvents + i + 1),
					ExecId:         "task-concurrent",
				}
				if err := log.Append(ctx, ev); err != nil {
					t.Errorf("concurrent append failed: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()

	events, err := log.Events(ctx, "conv-concurrent")
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

	events, err := log.Events(ctx, "conv-1")
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
