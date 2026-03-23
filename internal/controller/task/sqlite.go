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
	"database/sql"
	"fmt"
	"time"

	"github.com/google/ax/proto"
	_ "modernc.org/sqlite"
)

// SQLiteEventLog is a durable EventLog that persists events in a SQLite database.
// It is safe for concurrent use.
type SQLiteEventLog struct {
	db *sql.DB
}

const sqliteBusyTimeout = 10 * time.Second

// OpenSQLiteEventLog opens (or creates) a SQLite database at path and initializes the event log schema.
func OpenSQLiteEventLog(path string) (*SQLiteEventLog, error) {
	dsn := fmt.Sprintf("%s?_pragma=busy_timeout(%d)", path, sqliteBusyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite_eventlog: open %s: %w", dsn, err)
	}

	// Create table if it doesn't exist
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS event_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			exec_id TEXT NOT NULL,
			state INTEGER NOT NULL,
			payload TEXT NOT NULL,
			timestamp DATETIME NOT NULL
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite_eventlog: create table: %w", err)
	}

	// Create index if it doesn't exist
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_event_log_exec_id ON event_log(exec_id)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite_eventlog: create index: %w", err)
	}

	return &SQLiteEventLog{db: db}, nil
}

// Append serializes the event to JSON and inserts it into the database.
func (l *SQLiteEventLog) Append(ctx context.Context, event *proto.ExecutionEvent) error {
	payload, err := marshalOpts.Marshal(event)
	if err != nil {
		return fmt.Errorf("sqlite_eventlog: marshal: %w", err)
	}

	var timestamp time.Time
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	} else {
		// Fallback if not set, though caller usually sets it.
		timestamp = time.Now()
	}

	_, err = l.db.ExecContext(ctx,
		"INSERT INTO event_log (exec_id, state, payload, timestamp) VALUES (?, ?, ?, ?)",
		event.ExecId, event.State, string(payload), timestamp)

	if err != nil {
		return fmt.Errorf("sqlite_eventlog: insert: %w", err)
	}

	return nil
}

// Events retrieves all events from the database ordered by insertion order.
func (l *SQLiteEventLog) Events(ctx context.Context, id string) ([]*proto.ExecutionEvent, error) {
	rows, err := l.db.QueryContext(ctx, "SELECT payload FROM event_log WHERE exec_id = ?", id)
	if err != nil {
		return nil, fmt.Errorf("sqlite_eventlog: query: %w", err)
	}
	defer rows.Close()

	var events []*proto.ExecutionEvent
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("sqlite_eventlog: scan: %w", err)
		}

		ev := &proto.ExecutionEvent{}
		if err := unmarshalOpts.Unmarshal([]byte(payload), ev); err != nil {
			// Similar to FileEventLog, skip lines that cannot be decoded.
			continue
		}
		events = append(events, ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite_eventlog: iterate: %w", err)
	}

	return events, nil
}

// EventsByPrefix retrieves all events from the database matching a exec_id prefix, ordered by insertion order.
func (l *SQLiteEventLog) EventsByPrefix(ctx context.Context, prefix string) ([]*proto.ExecutionEvent, error) {
	rows, err := l.db.QueryContext(ctx, "SELECT payload FROM event_log WHERE exec_id LIKE ? ORDER BY id", prefix+"%")
	if err != nil {
		return nil, fmt.Errorf("sqlite_eventlog: query: %w", err)
	}
	defer rows.Close()

	var events []*proto.ExecutionEvent
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, fmt.Errorf("sqlite_eventlog: scan: %w", err)
		}

		ev := &proto.ExecutionEvent{}
		if err := unmarshalOpts.Unmarshal([]byte(payload), ev); err != nil {
			continue
		}
		events = append(events, ev)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sqlite_eventlog: iterate: %w", err)
	}

	return events, nil
}

// Close releases the database connection.
func (l *SQLiteEventLog) Close() error {
	return l.db.Close()
}
