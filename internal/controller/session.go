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
	eventLogFactory eventlog.EventLogFactory
}

// NewSessionManager creates a new session manager with a custom EventLog factory.
func NewSessionManager(factory eventlog.EventLogFactory) *SessionManager {
	return &SessionManager{
		sessions:        make(map[string]*Session),
		eventLogFactory: factory,
	}
}

// NewSession creates a new session with the given ID.
func (sm *SessionManager) NewSession(sessionID string) (*Session, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if session already ok
	if _, ok := sm.sessions[sessionID]; ok {
		return nil, fmt.Errorf("session %s already exists", sessionID)
	}

	// Create event log for this session using the factory
	el, err := sm.eventLogFactory(sessionID)
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
func (sm *SessionManager) LoadSessionFromCheckpoint(ctx context.Context, sessionID string, checkpointID string) (*Session, error) {
	if err := validateID(sessionID); err != nil {
		return nil, err
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Check if already loaded - remove it to reload fresh from checkpoint
	delete(sm.sessions, sessionID)

	// Open event log for replay using the factory
	el, err := sm.eventLogFactory(sessionID)
	if err != nil {
		return nil, fmt.Errorf("failed to open event log for replay: %w", err)
	}

	events, state, err := el.LoadEvents(ctx, checkpointID)
	if err != nil {
		return nil, fmt.Errorf("failed to get event log entries: %w", err)
	}

	// Reconstruct session state from event log
	session := &Session{
		id:             sessionID,
		state:          state,
		eventLog:       el,
		waitingAgents:  make(map[string][]*proto.Content),
		messageHistory: []*proto.Content{},
		checkpointIDs:  make(map[string]struct{}),
	}

	// Replay entries to rebuild state
	for _, e := range events {
		switch x := e.Kind.(type) {
		case *proto.Event_AgentCallEvent:
			event := x.AgentCallEvent
			if event.AwaitingMore {
				session.waitingAgents[event.Sender] = append(
					session.waitingAgents[event.Sender], event.Contents...)
			} else {
				// If buffer already exists, append it to the history first.
				// Then merge the new contents to the message history.
				// Once we don't wait for new contents from an agent,
				// we don't care about the origin of the contents anymore.
				if len(session.waitingAgents[event.Sender]) > 0 {
					session.messageHistory = append(
						session.messageHistory, session.waitingAgents[event.Sender]...)
				}
				session.messageHistory = append(
					session.messageHistory, event.Contents...)

				// Cleanup the waiting buffer, it's now a part of the overall history.
				delete(session.waitingAgents, event.Sender)
			}
		case *proto.Event_SessionStateEvent:
			session.state = x.SessionStateEvent.State
		case *proto.Event_HandoffEvent:
			return nil, fmt.Errorf("HandoffEvent is not yet supported")
		case *proto.Event_ContentEvent:
			session.messageHistory = append(session.messageHistory, x.ContentEvent.Contents...)
		default:
			return nil, fmt.Errorf("unknown event kind: %v", e.Kind)
		}

		if e.CheckpointId != "" {
			session.checkpointIDs[e.CheckpointId] = struct{}{}
		}
	}

	sm.sessions[sessionID] = session
	return session, nil
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

func (s *Session) CheckpointIDs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// TODO(jbd): Not ordered, but is still useful
	// for introspection capabilities.

	var checkpointIDs []string
	for checkpointID := range s.checkpointIDs {
		checkpointIDs = append(checkpointIDs, checkpointID)
	}
	return checkpointIDs
}
