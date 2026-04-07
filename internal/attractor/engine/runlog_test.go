// Tests for the RunLog structured event writer.
package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunLog_EmitsEvents(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRunLog(dir, "test-run-1")
	if err != nil {
		t.Fatal(err)
	}
	rl.Info("engine", "", "run.started", "Run started", map[string]any{"workspace": "/tmp"})
	rl.Info("engine", "detect", "node.started", "Executing: detect")
	rl.Info("tool", "detect", "stdout", "Detected build system: go")
	rl.Warn("engine", "detect", "node.completed", "Node detect: warning", map[string]any{"status": "success"})
	rl.Error("engine", "", "run.error", "Something went wrong")
	if err := rl.Close(); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "run.log"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d: %s", len(lines), string(data))
	}

	// Verify first event structure.
	var ev RunLogEvent
	if err := json.Unmarshal([]byte(lines[0]), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Level != "info" || ev.Source != "engine" || ev.Event != "run.started" {
		t.Errorf("unexpected first event: %+v", ev)
	}
	if ev.Data["workspace"] != "/tmp" {
		t.Errorf("expected workspace=/tmp in data, got %v", ev.Data)
	}

	// Verify third event is tool stdout.
	var ev3 RunLogEvent
	if err := json.Unmarshal([]byte(lines[2]), &ev3); err != nil {
		t.Fatal(err)
	}
	if ev3.Source != "tool" || ev3.Node != "detect" || ev3.Event != "stdout" {
		t.Errorf("unexpected third event: %+v", ev3)
	}
}

func TestLineWriter_EmitsLines(t *testing.T) {
	dir := t.TempDir()
	rl, err := NewRunLog(dir, "test-run")
	if err != nil {
		t.Fatal(err)
	}

	outFile, err := os.Create(filepath.Join(dir, "stdout.log"))
	if err != nil {
		t.Fatal(err)
	}
	lw := NewLineWriter(outFile, rl, "build", "stdout")

	// Write data in chunks that don't align with newlines.
	lw.Write([]byte("line one\nli"))
	lw.Write([]byte("ne two\nline"))
	lw.Write([]byte(" three"))
	lw.Flush()
	outFile.Close()
	rl.Close()

	// Verify the file got all the data.
	data, _ := os.ReadFile(filepath.Join(dir, "stdout.log"))
	if string(data) != "line one\nline two\nline three" {
		t.Errorf("unexpected file contents: %q", string(data))
	}

	// Verify RunLog got 3 line events.
	logData, _ := os.ReadFile(filepath.Join(dir, "run.log"))
	lines := strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 RunLog lines, got %d: %s", len(lines), string(logData))
	}
	var ev RunLogEvent
	json.Unmarshal([]byte(lines[0]), &ev)
	if ev.Source != "tool" || ev.Node != "build" || ev.Event != "stdout" || ev.Message != "line one" {
		t.Errorf("unexpected event: %+v", ev)
	}
	json.Unmarshal([]byte(lines[2]), &ev)
	if ev.Message != "line three" {
		t.Errorf("expected 'line three', got %q", ev.Message)
	}
}

func TestRunLog_NilSafe(t *testing.T) {
	var rl *RunLog
	// These should not panic.
	rl.Info("engine", "", "test", "msg")
	rl.Warn("engine", "", "test", "msg")
	rl.Error("engine", "", "test", "msg")
	rl.Emit("info", "engine", "", "test", "msg", nil)
	if err := rl.Close(); err != nil {
		t.Fatal(err)
	}
}
