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
