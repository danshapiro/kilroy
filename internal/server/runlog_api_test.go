// Tests for the run.log API endpoint filtering and reading.
package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadFilteredRunLog_NoFilters(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	lines := []string{
		`{"ts":"2026-04-07T12:00:00.000Z","level":"info","source":"engine","node":"","event":"run.started","msg":"Run started"}`,
		`{"ts":"2026-04-07T12:00:01.000Z","level":"info","source":"tool","node":"build","event":"stdout","msg":"compiling..."}`,
		`{"ts":"2026-04-07T12:00:02.000Z","level":"info","source":"engine","node":"build","event":"node.completed","msg":"done"}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	os.WriteFile(logPath, []byte(data), 0o644)

	events, err := readFilteredRunLog(logPath, "", "", "", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestReadFilteredRunLog_NodeFilter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	lines := []string{
		`{"ts":"2026-04-07T12:00:00.000Z","level":"info","source":"engine","node":"","event":"run.started","msg":"Run started"}`,
		`{"ts":"2026-04-07T12:00:01.000Z","level":"info","source":"tool","node":"build","event":"stdout","msg":"compiling..."}`,
		`{"ts":"2026-04-07T12:00:02.000Z","level":"info","source":"engine","node":"test","event":"node.started","msg":"testing"}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	os.WriteFile(logPath, []byte(data), 0o644)

	events, err := readFilteredRunLog(logPath, "build", "", "", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event for node=build, got %d", len(events))
	}
}

func TestReadFilteredRunLog_SourceFilter(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	lines := []string{
		`{"ts":"2026-04-07T12:00:00.000Z","level":"info","source":"engine","node":"","event":"run.started","msg":"Run started"}`,
		`{"ts":"2026-04-07T12:00:01.000Z","level":"info","source":"tool","node":"build","event":"stdout","msg":"compiling..."}`,
		`{"ts":"2026-04-07T12:00:02.000Z","level":"info","source":"tool","node":"build","event":"stderr","msg":"warning: unused"}`,
	}
	data := ""
	for _, l := range lines {
		data += l + "\n"
	}
	os.WriteFile(logPath, []byte(data), 0o644)

	events, err := readFilteredRunLog(logPath, "", "tool", "", time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 tool events, got %d", len(events))
	}
}

func TestReadFilteredRunLog_TailN(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "run.log")
	data := ""
	for i := 0; i < 10; i++ {
		line := map[string]any{
			"ts": "2026-04-07T12:00:00.000Z", "level": "info",
			"source": "engine", "node": "", "event": "test", "msg": "event",
		}
		b, _ := json.Marshal(line)
		data += string(b) + "\n"
	}
	os.WriteFile(logPath, []byte(data), 0o644)

	events, err := readFilteredRunLog(logPath, "", "", "", time.Time{}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 tail events, got %d", len(events))
	}
}
