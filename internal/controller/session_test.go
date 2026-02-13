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
	"fmt"
	"sync"
	"testing"

	"github.com/google/gar/internal/eventlog"
	"github.com/google/gar/proto"
)

// MockEventLog implements eventlog.EventLog for testing.
type MockEventLog struct {
	sessionID string
	events    []*proto.Event
	mu        sync.Mutex
	closed    bool
}

func (m *MockEventLog) AppendEvent(ctx context.Context, e *proto.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return fmt.Errorf("log closed")
	}
	m.events = append(m.events, e)
	return nil
}

func (m *MockEventLog) LoadEvents(ctx context.Context, checkpointID string) ([]*proto.Event, proto.State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	var events []*proto.Event
	var state proto.State
	for _, e := range m.events {
		switch x := e.Kind.(type) {
		case *proto.Event_SessionStateEvent:
			state = x.SessionStateEvent.State
		}
		events = append(events, e)
		if checkpointID != "" && e.CheckpointId == checkpointID {
			break
		}
	}
	return events, state, nil
}

func (m *MockEventLog) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	return nil
}

func (m *MockEventLog) SessionID() string {
	return m.sessionID
}

func TestSessionManager_ForkSession_Success(t *testing.T) {
	ctx := context.Background()
	
	// Create a storage for mocks to simulate persistence
	storage := make(map[string]*MockEventLog)
	factory := func(sessionID string) (eventlog.EventLog, error) {
		if el, ok := storage[sessionID]; ok {
			return el, nil
		}
		el := &MockEventLog{sessionID: sessionID}
		storage[sessionID] = el
		return el, nil
	}

	sm := &SessionManager{
		sessions:        make(map[string]*Session),
		eventLogBuilder: factory,
	}

	sourceID := "source"
	checkpointID := "cp1"
	newID := "fork"

	// Setup source session with events
	sourceEL, _ := factory(sourceID)
	sourceEvents := []*proto.Event{
		{SessionId: sourceID, CheckpointId: checkpointID, Kind: &proto.Event_ContentEvent{ContentEvent: &proto.ContentEvent{Contents: []*proto.Content{{Role: "user"}}}}},
		{SessionId: sourceID, Kind: &proto.Event_SessionStateEvent{SessionStateEvent: &proto.SessionStateEvent{State: proto.State_STATE_COMPLETED}}},
	}
	for _, e := range sourceEvents {
		sourceEL.AppendEvent(ctx, e)
	}

	// Fork
	session, err := sm.ForkSession(ctx, sourceID, checkpointID, newID)
	if err != nil {
		t.Fatalf("ForkSession failed: %v", err)
	}

	if session.ID() != newID {
		t.Errorf("expected session ID %s, got %s", newID, session.ID())
	}

	// Verify events were replayed to new ID
	forkEL := storage[newID]
	if len(forkEL.events) != 1 { // Only one event up to checkpoint
		t.Errorf("expected 1 event replayed, got %d", len(forkEL.events))
	}
	if forkEL.events[0].SessionId != newID {
		t.Errorf("expected replayed event to have session ID %s, got %s", newID, forkEL.events[0].SessionId)
	}
}


func TestSessionManager_ForkSession_Concurrent(t *testing.T) {
	// Test if multiple concurrent forks to the same ID handle memory lock correctly
	ctx := context.Background()
	storage := make(map[string]*MockEventLog)
	var mu sync.Mutex
	factory := func(sessionID string) (eventlog.EventLog, error) {
		mu.Lock()
		defer mu.Unlock()
		if el, ok := storage[sessionID]; ok {
			return el, nil
		}
		el := &MockEventLog{sessionID: sessionID}
		storage[sessionID] = el
		return el, nil
	}

	sm := &SessionManager{
		sessions:        make(map[string]*Session),
		eventLogBuilder: factory,
	}

	sourceID := "source"
	newID := "shared-fork"

	// Setup source
	sourceEL, _ := factory(sourceID)
	sourceEL.AppendEvent(ctx, &proto.Event{SessionId: sourceID, Kind: &proto.Event_ContentEvent{ContentEvent: &proto.ContentEvent{}}})

	// Run multiple forks concurrently
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sm.ForkSession(ctx, sourceID, "", newID)
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)

	successCount := 0
	collisionCount := 0
	for err := range errs {
		if err != nil {
			collisionCount++
		}
	}
	successCount = 10 - collisionCount

	if successCount != 1 {
		t.Errorf("expected exactly 1 successful fork, got %d", successCount)
	}
}
