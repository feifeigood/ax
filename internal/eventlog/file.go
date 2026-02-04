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

package eventlog

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/google/gar/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// FileEventLog represents a file-based event log for a single session.
// It provides append-only durability with JSON Lines format.
type FileEventLog struct {
	mu        sync.Mutex
	sessionID string
	filePath  string
	file      *os.File
	writer    *bufio.Writer
}

// FileConfig configures a FileEventLog instance.
type FileConfig struct {
	SessionID string
	Dir       string // Directory where event log files are stored
}

// NewFileEventLog creates a new file-based EventLog instance for the given session.
// It creates the event log directory and file if they don't exist.
func NewFileEventLog(config FileConfig) (*FileEventLog, error) {
	if config.SessionID == "" {
		return nil, fmt.Errorf("session ID cannot be empty")
	}
	if config.Dir == "" {
		config.Dir = "eventlog"
	}

	// Create event log directory if it doesn't exist
	if err := os.MkdirAll(config.Dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create event log directory: %w", err)
	}

	filePath := filepath.Join(config.Dir, fmt.Sprintf("%s.log", config.SessionID))

	// Open file in append mode, create if doesn't exist
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open event log file: %w", err)
	}

	return &FileEventLog{
		sessionID: config.SessionID,
		filePath:  filePath,
		file:      file,
		writer:    bufio.NewWriter(file),
	}, nil
}

// NewFileConfig creates a new file-based EventLog instance for the given session.
// This is a convenience function that wraps NewFileEventLog.
// Deprecated: Use NewFileEventLog for clarity, or implement your own EventLog.
func NewFileConfig(config FileConfig) (EventLog, error) {
	return NewFileEventLog(config)
}

func (e *FileEventLog) AppendEvent(ctx context.Context, event *proto.Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	jsonData, err := protojson.Marshal(event)
	if err != nil {
		return fmt.Errorf("failed to marshal entry: %w", err)
	}
	if _, err := e.writer.Write(jsonData); err != nil {
		return fmt.Errorf("failed to write entry: %w", err)
	}
	if _, err := e.writer.WriteString("\n"); err != nil {
		return fmt.Errorf("failed to write newline: %w", err)
	}

	// Flush to ensure durability
	if err := e.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush: %w", err)
	}
	if err := e.file.Sync(); err != nil {
		return fmt.Errorf("failed to sync: %w", err)
	}
	return nil
}

func (e *FileEventLog) LoadEvents(ctx context.Context, checkpointID string) ([]*proto.Event, proto.State, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	var state proto.State
	if err := e.writer.Flush(); err != nil {
		return nil, 0, fmt.Errorf("failed to flush before reading: %w", err)
	}

	f, err := os.Open(e.filePath)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open event log file for reading: %w", err)
	}
	defer f.Close()

	var events []*proto.Event
	scanner := bufio.NewScanner(f)
	checkpointFound := false

	for scanner.Scan() {
		line := scanner.Bytes()

		var e proto.Event
		if err := protojson.Unmarshal(line, &e); err != nil {
			return nil, 0, fmt.Errorf("failed to unmarshal event: %w", err)
		}

		switch x := e.Kind.(type) {
		case *proto.Event_SessionStateEvent:
			state = x.SessionStateEvent.State
		}
		events = append(events, &e)
		if checkpointID != "" && e.CheckpointId == checkpointID {
			checkpointFound = true
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("error reading event log file: %w", err)
	}
	if checkpointID != "" && !checkpointFound {
		return nil, 0, fmt.Errorf("checkpoint ID %s not found in event log", checkpointID)
	}

	return events, state, nil
}

// Close closes the event log file.
func (e *FileEventLog) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.writer.Flush(); err != nil {
		return fmt.Errorf("failed to flush on close: %w", err)
	}
	if err := e.file.Close(); err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	return nil
}

// FilePath returns the path to the event log file.
// This method is specific to FileEventLog and not part of the EventLog interface.
func (e *FileEventLog) FilePath() string {
	return e.filePath
}

// SessionID returns the session ID for this event log.
func (e *FileEventLog) SessionID() string {
	return e.sessionID
}
