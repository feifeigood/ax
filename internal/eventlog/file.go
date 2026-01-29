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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/gar/proto"
)

// FileEventLog represents a file-based event log for a single session.
// It provides append-only durability with JSON Lines format.
type FileEventLog struct {
	mu        sync.Mutex
	sessionID string
	filePath  string
	file      *os.File
	writer    *bufio.Writer
	sequence  int64 // Monotonic sequence counter
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

	// Read existing entries to determine the current sequence number
	var sequence int64
	if fileInfo, err := os.Stat(filePath); err == nil && fileInfo.Size() > 0 {
		readFile, err := os.Open(filePath)
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to read existing log for sequence: %w", err)
		}
		scanner := bufio.NewScanner(readFile)
		for scanner.Scan() {
			var entry Entry
			if err := json.Unmarshal(scanner.Bytes(), &entry); err == nil {
				if entry.Sequence > sequence {
					sequence = entry.Sequence
				}
			}
		}
		readFile.Close()
	}

	return &FileEventLog{
		sessionID: config.SessionID,
		filePath:  filePath,
		file:      file,
		writer:    bufio.NewWriter(file),
		sequence:  sequence,
	}, nil
}

// NewFileConfig creates a new file-based EventLog instance for the given session.
// This is a convenience function that wraps NewFileEventLog.
// Deprecated: Use NewFileEventLog for clarity, or implement your own EventLog.
func NewFileConfig(config FileConfig) (EventLog, error) {
	return NewFileEventLog(config)
}

// Append writes an entry to the event log.
// Entries are written in JSON Lines format with atomic appends.
func (e *FileEventLog) Append(entryType EventType, checkpointID string, data map[string]any) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry := Entry{
		SessionID:    e.sessionID,
		Timestamp:    time.Now(),
		Sequence:     e.sequence,
		Type:         entryType,
		CheckpointID: checkpointID,
		Data:         data,
	}

	// Increment sequence for next entry
	e.sequence++

	jsonData, err := json.Marshal(entry)
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

// AppendContent writes a content message to the event log with a checkpoint UUID.
func (e *FileEventLog) AppendContent(ctx context.Context, direction EventType, checkpointID string, content *proto.Content) error {
	data := map[string]any{
		"role":     content.Role,
		"type":     content.Type,
		"mimetype": content.Mimetype,
		"data":     content.Data,
	}
	return e.Append(direction, checkpointID, data)
}

func (e *FileEventLog) AppendState(ctx context.Context, s proto.State) error {
	data := map[string]any{
		"state": s,
	}
	return e.Append(EventTypeState, "", data)
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

// RetrieveEntries reads and returns all entries from the event log file.
// Returns entries in order.
func (e *FileEventLog) RetrieveEntries(ctx context.Context) ([]Entry, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	// Flush any pending writes first
	if err := e.writer.Flush(); err != nil {
		return nil, fmt.Errorf("failed to flush before reading: %w", err)
	}

	// Check if file exists
	if _, err := os.Stat(e.filePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("event log file does not exist")
	}

	// Open file for reading
	readFile, err := os.Open(e.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open event log file for reading: %w", err)
	}
	defer readFile.Close()

	var entries []Entry
	scanner := bufio.NewScanner(readFile)

	lineNum := 0
	var expectedSequence int64 = 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry at line %d: %w", lineNum, err)
		}

		// Validate sequence number
		if entry.Sequence != expectedSequence {
			return nil, fmt.Errorf("sequence number mismatch at line %d: expected %d, got %d", lineNum, expectedSequence, entry.Sequence)
		}
		expectedSequence++

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading event log file: %w", err)
	}

	return entries, nil
}
