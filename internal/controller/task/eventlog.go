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
	"bufio"
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/google/gar/proto"
	"google.golang.org/protobuf/encoding/protojson"
)

// EventLog is the persistent, append-only record of all actions taken in an
// exec. Every entry is an atomic step: replaying the log in order brings
// the executor back to a consistent state from which execution can resume.
type EventLog interface {
	// Append adds an event to the end of the log.
	// Implementations must be safe for concurrent use.
	Append(ctx context.Context, event *proto.ExecutionEvent) error

	// Events returns all events recorded so far, in append order.
	Events(ctx context.Context) ([]*proto.ExecutionEvent, error)

	// Close releases the underlying resources and closes the log.
	Close() error
}

// FileEventLog is a durable EventLog that writes one JSON object per line to
// a file. Each execution should use its own file. The file is created if it does
// not exist and is opened for appending, so existing events survive restarts.
//
// The format is newline-delimited JSON (NDJSON): every Append call writes one
// complete JSON object followed by a newline, making both concurrent writes
// and crash recovery safe.
type FileEventLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

// OpenFileEventLog opens (or creates) the log file at path.
// Call Close when the execution is done to release the file handle.
func OpenFileEventLog(path string) (*FileEventLog, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("eventlog: open %s: %w", path, err)
	}
	return &FileEventLog{path: path, f: f}, nil
}

var marshalOpts = protojson.MarshalOptions{UseProtoNames: true}
var unmarshalOpts = protojson.UnmarshalOptions{DiscardUnknown: true}

// Append serialises event as a single JSON line and syncs to disk.
func (l *FileEventLog) Append(_ context.Context, event *proto.ExecutionEvent) error {
	line, err := marshalOpts.Marshal(event)
	if err != nil {
		return fmt.Errorf("eventlog: marshal: %w", err)
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("eventlog: write %s: %w", l.path, err)
	}
	// Sync after every write so a crash does not leave a partial line.
	return l.f.Sync()
}

// Events reads all complete JSON lines from the beginning of the file.
func (l *FileEventLog) Events(_ context.Context) ([]*proto.ExecutionEvent, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if _, err := l.f.Seek(0, 0); err != nil {
		return nil, fmt.Errorf("eventlog: seek %s: %w", l.path, err)
	}

	var events []*proto.ExecutionEvent
	scanner := bufio.NewScanner(l.f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		ev := &proto.ExecutionEvent{}
		if err := unmarshalOpts.Unmarshal(line, ev); err != nil {
			// Skip lines that could not be decoded (e.g. truncated by crash).
			continue
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

// Close releases the underlying file handle.
func (l *FileEventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.f.Close()
}
