// RunLog writes a chronological, newline-delimited JSON activity log per run.
// Each line is a timestamped event — the canonical source of truth for what happened.
package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RunLog writes structured events to {logs_root}/run.log.
type RunLog struct {
	f      *os.File
	mu     sync.Mutex
	runID  string
	closed bool
}

// RunLogEvent is the canonical schema for a run log entry.
type RunLogEvent struct {
	Timestamp string         `json:"ts"`
	Level     string         `json:"level"`
	Source    string         `json:"source"`
	Node      string         `json:"node"`
	Event     string         `json:"event"`
	Message   string         `json:"msg"`
	Data      map[string]any `json:"data,omitempty"`
}

// NewRunLog creates a RunLog writing to {logsRoot}/run.log.
func NewRunLog(logsRoot, runID string) (*RunLog, error) {
	path := filepath.Join(logsRoot, "run.log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run.log: %w", err)
	}
	return &RunLog{f: f, runID: runID}, nil
}

// Emit writes a single event to the log.
func (l *RunLog) Emit(level, source, node, event, msg string, data map[string]any) {
	if l == nil {
		return
	}
	ev := RunLogEvent{
		Timestamp: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Level:     level,
		Source:    source,
		Node:      node,
		Event:     event,
		Message:   msg,
		Data:      data,
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	_, _ = l.f.Write(append(b, '\n'))
}

// Info emits an info-level event.
func (l *RunLog) Info(source, node, event, msg string, data ...map[string]any) {
	var d map[string]any
	if len(data) > 0 {
		d = data[0]
	}
	l.Emit("info", source, node, event, msg, d)
}

// Warn emits a warn-level event.
func (l *RunLog) Warn(source, node, event, msg string, data ...map[string]any) {
	var d map[string]any
	if len(data) > 0 {
		d = data[0]
	}
	l.Emit("warn", source, node, event, msg, d)
}

// Error emits an error-level event.
func (l *RunLog) Error(source, node, event, msg string, data ...map[string]any) {
	var d map[string]any
	if len(data) > 0 {
		d = data[0]
	}
	l.Emit("error", source, node, event, msg, d)
}

// contextUpdateKeys returns the keys from a context updates map.
func contextUpdateKeys(updates map[string]any) []string {
	keys := make([]string, 0, len(updates))
	for k := range updates {
		keys = append(keys, k)
	}
	return keys
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Close flushes and closes the underlying file.
func (l *RunLog) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	return l.f.Close()
}
