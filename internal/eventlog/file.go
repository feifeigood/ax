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

// Append writes an entry to the event log.
// Entries are written in JSON Lines format with atomic appends.
func (e *FileEventLog) Append(entryType EventType, checkpointID string, data map[string]interface{}) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	entry := Entry{
		SessionID:    e.sessionID,
		Timestamp:    time.Now(),
		Type:         entryType,
		CheckpointID: checkpointID,
		Data:         data,
	}

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
	data := map[string]interface{}{
		"role":     content.Role,
		"type":     content.Type,
		"mimetype": content.Mimetype,
		"data":     content.Data,
	}
	return e.Append(direction, checkpointID, data)
}

// AppendLifecycleEvent writes a lifecycle event to the event log.
// Lifecycle events don't have checkpoint IDs.
func (e *FileEventLog) AppendLifecycleEvent(ctx context.Context, event *proto.LifecycleEvent) error {
	var timestampSeconds int64
	var timestampNanos int32
	if event.Timestamp != nil {
		timestampSeconds = event.Timestamp.Seconds
		timestampNanos = event.Timestamp.Nanos
	}

	data := map[string]interface{}{
		"event_type":        event.EventType,
		"agent_id":          event.AgentId,
		"timestamp_seconds": timestampSeconds,
		"timestamp_nanos":   timestampNanos,
		"metadata":          event.Metadata,
	}
	return e.Append(EventTypeLifecycle, "", data)
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
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()

		var entry Entry
		if err := json.Unmarshal(line, &entry); err != nil {
			return nil, fmt.Errorf("failed to unmarshal entry at line %d: %w", lineNum, err)
		}

		entries = append(entries, entry)
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading event log file: %w", err)
	}

	return entries, nil
}
