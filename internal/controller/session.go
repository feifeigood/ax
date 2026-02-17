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

	"github.com/google/gar/internal/eventlog"
	"github.com/google/gar/proto"
	pbproto "google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Session represents an agentic loop execution session.
// It maintains in-memory state and uses event log for durability.
type Session struct {
	id string

	mu             sync.RWMutex
	eventLog       eventlog.EventLog
	state          proto.State
	messageHistory []*proto.Content
	waitingAgents  map[string][]*proto.Content // by agent ID
	checkpointIDs  map[string]struct{}         // checkpoint UUIDs
}

// SessionManager manages multiple sessions.
type SessionManager struct {
	mu              sync.RWMutex
	sessions        map[string]*Session
	eventLogBuilder eventlog.EventLogBuilder
}

// NewSessionManager creates a new session manager with a custom EventLog builder.
func NewSessionManager(builder eventlog.EventLogBuilder) *SessionManager {
	return &SessionManager{
		sessions:        make(map[string]*Session),
		eventLogBuilder: builder,
	}
}

// NewSession creates a new session with the given ID.
func (sm *SessionManager) NewSession(sessionID string) (*Session, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if session already exists
	if _, ok := sm.sessions[sessionID]; ok {
		return nil, fmt.Errorf("session %s already exists", sessionID)
	}

	// Create event log for this session using the builder.
	el, err := sm.eventLogBuilder(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to create event log: %w", err)
	}

	session := &Session{
		id:             sessionID,
		state:          proto.State_STATE_UNSPECIFIED,
		messageHistory: []*proto.Content{},
		waitingAgents:  make(map[string][]*proto.Content),
		checkpointIDs:  make(map[string]struct{}),
		eventLog:       el,
	}

	sm.sessions[sessionID] = session
	return session, nil
}

// LoadSession loads an existing session from event log.
func (sm *SessionManager) LoadSession(ctx context.Context, sessionID string) (*Session, error) {
	return sm.LoadSessionFromCheckpoint(ctx, sessionID, "")
}

// LoadSessionFromCheckpoint loads an existing session from event log up to a specific checkpoint.
// If checkpointID is empty, loads to the latest state.
// If checkpointID is provided, loads up to and including that checkpoint UUID.
func (sm *SessionManager) LoadSessionFromCheckpoint(ctx context.Context, sessionID, checkpointID string) (*Session, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}

	// 1. Load events from storage (IO) - No Lock
	// We do this without holding the lock to allow concurrent reads/writes on other sessions.
	el, events, state, err := sm.loadEvents(ctx, sessionID, checkpointID)
	if err != nil {
		return nil, err
	}

	// 2. Construct session state in memory - No Lock
	session := &Session{
		id:             sessionID,
		state:          state,
		eventLog:       el,
		waitingAgents:  make(map[string][]*proto.Content),
		messageHistory: []*proto.Content{},
		checkpointIDs:  make(map[string]struct{}),
	}

	err = session.reconstructState(events)
	if err != nil {
		_ = el.Close()
		return nil, err
	}

	// 3. Commit to manager (Write Lock)
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Overwrite existing session (atomic switch)
	// If a session existed, it is replaced by the reloaded version.
	// The old session instance (if any) is discarded from the map.
	// Note: We don't explicitly close the session's event log here because it might be in use elsewhere.
	sm.sessions[sessionID] = session
	return session, nil
}

// loadEvents opens the event log and loads events up to the checkpoint.
func (sm *SessionManager) loadEvents(ctx context.Context, sessionID, checkpointID string) (eventlog.EventLog, []*proto.Event, proto.State, error) {
	// Open event log for replay using the factory
	el, err := sm.eventLogBuilder(sessionID)
	if err != nil {
		return nil, nil, proto.State_STATE_UNSPECIFIED, fmt.Errorf("failed to open event log for replay: %w", err)
	}

	events, state, err := el.LoadEvents(ctx, checkpointID)
	if err != nil {
		// Clean up potentially opened event log
		_ = el.Close()
		return nil, nil, proto.State_STATE_UNSPECIFIED, fmt.Errorf("failed to get event log entries: %w", err)
	}

	return el, events, state, nil
}

// ForkSession creates a new session by forking from a source session's checkpoint.
func (sm *SessionManager) ForkSession(ctx context.Context, sourceSessionID, sourceCheckpointID, newSessionID string) (*Session, error) {
	if err := validateID(sourceSessionID); err != nil {
		return nil, fmt.Errorf("invalid source session ID: %w", err)
	}
	if err := validateID(newSessionID); err != nil {
		return nil, fmt.Errorf("invalid new session ID: %w", err)
	}

	// 1. Check if session already exists in backend storage
	newEL, existingEvents, _, err := sm.loadEvents(ctx, newSessionID, "")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize new session: %w", err)
	}
	if len(existingEvents) > 0 {
		_ = newEL.Close()
		return nil, fmt.Errorf("session %s already exists in storage", newSessionID)
	}

	// 2. Load Source Events
	sourceEL, sourceEvents, _, err := sm.loadEvents(ctx, sourceSessionID, sourceCheckpointID)
	if err != nil {
		_ = newEL.Close()
		return nil, fmt.Errorf("failed to load source events: %w", err)
	}
	// Close source EL immediately as we only need the events
	if sourceEL != nil {
		_ = sourceEL.Close()
	}

	// 3. Fork the events
	newEvents := make([]*proto.Event, 0, len(sourceEvents))
	for _, e := range sourceEvents {
		// Deep copy the event
		newEvent := pbproto.Clone(e).(*proto.Event)
		// Update SessionId for the new session
		newEvent.SessionId = newSessionID

		if err := newEL.AppendEvent(ctx, newEvent); err != nil {
			_ = newEL.Close()
			return nil, fmt.Errorf("failed to append forked event: %w", err)
		}

		newEvents = append(newEvents, newEvent)
	}

	// 4. Construct base session
	session := &Session{
		id:             newSessionID,
		state:          proto.State_STATE_UNSPECIFIED,
		messageHistory: []*proto.Content{},
		waitingAgents:  make(map[string][]*proto.Content),
		checkpointIDs:  make(map[string]struct{}),
		eventLog:       newEL,
	}

	// Reconstruct session from new events
	// Session state will be set to the latest state from events
	err = session.reconstructState(newEvents)
	if err != nil {
		_ = newEL.Close()
		return nil, err
	}

	// 5. Add new session to manager
	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, ok := sm.sessions[newSessionID]
	if ok {
		_ = newEL.Close()
		return nil, fmt.Errorf("failed to add new session to manager: %s", newSessionID)
	}

	sm.sessions[newSessionID] = session
	return session, nil
}

// Replay entries to rebuild state
func (s *Session) reconstructState(events []*proto.Event) error {
	for _, e := range events {
		switch x := e.Kind.(type) {
		case *proto.Event_AgentCallEvent:
			event := x.AgentCallEvent
			if event.AwaitingMore {
				s.waitingAgents[event.Sender] = append(
					s.waitingAgents[event.Sender], event.Contents...)
			} else {
				// If buffer already exists, append it to the history first.
				// Then merge the new contents to the message history.
				// Once we don't wait for new contents from an agent,
				// we don't care about the origin of the contents anymore.
				if len(s.waitingAgents[event.Sender]) > 0 {
					s.messageHistory = append(
						s.messageHistory, s.waitingAgents[event.Sender]...)
				}
				s.messageHistory = append(
					s.messageHistory, event.Contents...)

				// Cleanup the waiting buffer, it's now a part of the overall history.
				delete(s.waitingAgents, event.Sender)
			}
		case *proto.Event_SessionStateEvent:
			s.state = x.SessionStateEvent.State
		case *proto.Event_HandoffEvent:
			// TODO: Visit this section when resuming the waiting agents.
		case *proto.Event_ContentEvent:
			s.messageHistory = append(s.messageHistory, x.ContentEvent.Contents...)
		default:
			return fmt.Errorf("unknown event kind: %v", e.Kind)
		}

		if e.CheckpointId != "" {
			s.checkpointIDs[e.CheckpointId] = struct{}{}
		}
	}
	return nil
}

// GetSession retrieves a session by ID.
func (sm *SessionManager) GetSession(sessionID string) (*Session, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return nil, fmt.Errorf("session %s not found", sessionID)
	}

	return session, nil
}

// CloseSession closes a session and its event log.
func (sm *SessionManager) CloseSession(sessionID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return fmt.Errorf("session %s not found", sessionID)
	}

	if err := session.eventLog.Close(); err != nil {
		return fmt.Errorf("failed to close event log: %w", err)
	}

	delete(sm.sessions, sessionID)
	return nil
}

// CloseAll closes all active sessions.
func (sm *SessionManager) CloseAll() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for sessionID, session := range sm.sessions {
		if err := session.eventLog.Close(); err != nil {
			// Log error but continue closing other sessions
			_ = err
		}
		delete(sm.sessions, sessionID)
	}
}

func (s *Session) WriteAgentHandoff(ctx context.Context, source, target string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.eventLog.AppendEvent(ctx, &proto.Event{
		SessionId:           s.id,
		SenderId:            source,
		Timestamp:           timestamppb.Now(),
		ControllerTimestamp: timestamppb.Now(),
		Kind: &proto.Event_HandoffEvent{
			HandoffEvent: &proto.HandoffEvent{
				SourceAgentId: source,
				TargetAgentId: target,
			},
		},
	})
}

// WriteContent appends an incoming content message to the session.
// Creates a checkpoint only if checkpoint_id is provided in the content.
func (s *Session) WriteContent(ctx context.Context, sender string, checkpointID string, contents []*proto.Content) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if checkpointID != "" {
		if _, ok := s.checkpointIDs[checkpointID]; ok {
			return fmt.Errorf("checkpoint %s already exists", checkpointID)
		}
	}

	if err := s.eventLog.AppendEvent(ctx, &proto.Event{
		SessionId:           s.id,
		CheckpointId:        checkpointID,
		SenderId:            sender,
		Timestamp:           timestamppb.Now(),
		ControllerTimestamp: timestamppb.Now(),
		Kind: &proto.Event_ContentEvent{
			ContentEvent: &proto.ContentEvent{
				Contents: contents,
			},
		},
	}); err != nil {
		return err
	}

	s.messageHistory = append(s.messageHistory, contents...)
	if checkpointID != "" {
		s.checkpointIDs[checkpointID] = struct{}{}
	}
	return nil
}

// SetState updates the session state.
func (s *Session) SetState(ctx context.Context, state proto.State) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.eventLog.AppendEvent(ctx, &proto.Event{
		SessionId:           s.id,
		Timestamp:           timestamppb.Now(),
		ControllerTimestamp: timestamppb.Now(),
		Kind: &proto.Event_SessionStateEvent{
			SessionStateEvent: &proto.SessionStateEvent{
				State: state,
			},
		},
	}); err != nil {
		return err
	}

	s.state = state
	return nil
}

func (s *Session) WaitingAgents() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var agents []string
	for agentID := range s.waitingAgents {
		agents = append(agents, agentID)
	}
	return agents
}

func (s *Session) WaitingBuffer(agentID string) []*proto.Content {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.waitingAgents[agentID]
}

func (s *Session) ID() string {
	return s.id
}

func (s *Session) State() proto.State {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.state
}

func (s *Session) History() []*proto.Content {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.messageHistory
}
