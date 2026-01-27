// Package eventlog implements event logging for session durability.
// Event log entries use JSON Lines format with one log file per session.
package eventlog

import (
	"context"
	"time"

	"github.com/google/gar/proto"
)

// EventType represents the type of event stored in the event log.
type EventType string

const (
	EventTypeContentIn  EventType = "CONTENT_IN"
	EventTypeContentOut EventType = "CONTENT_OUT"
	// TODO(jbd): Add EventTypeCompaction.
)

// Entry represents a single entry in the event log.
type Entry struct {
	SessionID    string         `json:"session_id"`
	CheckpointID string         `json:"checkpoint_id,omitempty"` // UUID for checkpoint tracking
	Timestamp    time.Time      `json:"timestamp"`
	Sequence     int64          `json:"seq"` // Monotonic sequence number
	Type         EventType      `json:"type"`
	Data         map[string]any `json:"data"`
}

// EventLog is the interface that all event log implementations must satisfy.
// It provides methods for appending events, reading entries, and managing the log lifecycle.
type EventLog interface {
	// AppendContent appends a content message to the event log with a checkpoint UUID.
	AppendContent(ctx context.Context, t EventType, checkpointID string, content *proto.Content) error

	// RetrieveEntries returns all entries from the event log in order.
	RetrieveEntries(ctx context.Context) ([]Entry, error)

	// Close closes the event log and releases any resources.
	Close() error

	// SessionID returns the session ID for this event log.
	SessionID() string
}
