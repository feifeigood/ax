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

// Package eventlog implements event logging for session durability.
// Event log entries use JSON Lines format with one log file per session.
package eventlog

import (
	"context"

	"github.com/google/gar/proto"
)

// EventLog is the interface that all event log implementations must satisfy.
// It provides methods for appending events, reading entries, and managing the log lifecycle.
type EventLog interface {
	// AppendEvent appends an event to the log.
	AppendEvent(ctx context.Context, e *proto.Event) error

	// LoadEvents loads all events up to the checkpoint.
	// If no checkpoint is provided, it returns all events recorded.
	LoadEvents(ctx context.Context, checkpointID string) ([]*proto.Event, proto.State, error)

	// Close closes the event log and releases any resources.
	Close() error

	// SessionID returns the session ID for this event log.
	SessionID() string
}

// EventLogBuilder is a function that creates EventLog instances for sessions.
type EventLogBuilder func(sessionID string) (EventLog, error)
