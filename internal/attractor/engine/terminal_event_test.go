package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// readLastProgressEvent parses progress.ndjson and returns the last non-empty
// JSON event. It fails the test if the file is missing or contains no events.
func readLastProgressEvent(t *testing.T, progressPath string) map[string]any {
	t.Helper()
	f, err := os.Open(progressPath)
	if err != nil {
		t.Fatalf("open progress.ndjson: %v", err)
	}
	defer func() { _ = f.Close() }()

	var last map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("unmarshal progress line: %v (line=%q)", err, line)
		}
		last = ev
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan progress.ndjson: %v", err)
	}
	if last == nil {
		t.Fatal("progress.ndjson is empty or has no valid JSON lines")
	}
	return last
}

// TestProgressNDJSON_TerminalEvent_RunCompleted verifies that a successful run
// ends with a run_completed event as the last line of progress.ndjson with the
// correct fields (event, status, run_id, ts).
func TestProgressNDJSON_TerminalEvent_RunCompleted(t *testing.T) {
	// Minimal graph: start → exit (no agent nodes, no git required).
	dot := []byte(`
digraph T {
  graph [goal="terminal event test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logsRoot := t.TempDir()
	runID := fmt.Sprintf("term-ok-%d", time.Now().UnixNano())

	res, err := runForTest(t, ctx, dot, RunOptions{
		LogsRoot: logsRoot,
		RunID:    runID,
	})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	last := readLastProgressEvent(t, filepath.Join(res.LogsRoot, "progress.ndjson"))

	// The last line must be run_completed with correct fields.
	if last["event"] != "run_completed" {
		t.Fatalf("last progress event: got %q want %q", last["event"], "run_completed")
	}
	if last["status"] != "success" {
		t.Fatalf("last event status: got %q want %q", last["status"], "success")
	}
	if last["run_id"] != res.RunID {
		t.Fatalf("last event run_id: got %q want %q", last["run_id"], res.RunID)
	}
	ts, _ := last["ts"].(string)
	if strings.TrimSpace(ts) == "" {
		t.Fatal("last event ts is empty")
	}

	// final.json must already exist when run_completed is observed (ordering guarantee).
	finalPath := filepath.Join(res.LogsRoot, "final.json")
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final.json must exist before run_completed is emitted: %v", err)
	}
}

// TestProgressNDJSON_TerminalEvent_RunFailed verifies that a failed run ends
// with a run_failed event (status=fail) as the last line of progress.ndjson.
//
// The graph uses a parallel fan-in pattern where both branches fail with
// exit code 1. When the fan-in join node has no outgoing fail edge, the engine
// terminates with FinalFail, which triggers the run_failed terminal event.
func TestProgressNDJSON_TerminalEvent_RunFailed(t *testing.T) {
	dot := []byte(`
digraph T {
  graph [goal="terminal failure event test"]
  start [shape=Mdiamond]
  par   [shape=component]
  a     [shape=parallelogram, tool_command="exit 1", max_retries=0]
  b     [shape=parallelogram, tool_command="exit 1", max_retries=0]
  join  [shape=tripleoctagon, max_retries=0]
  exit  [shape=Msquare]
  start -> par
  par -> a
  par -> b
  a -> join
  b -> join
  join -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logsRoot := t.TempDir()
	runID := fmt.Sprintf("term-fail-%d", time.Now().UnixNano())

	_, err := runForTest(t, ctx, dot, RunOptions{
		LogsRoot: logsRoot,
		RunID:    runID,
	})
	if err == nil {
		t.Fatal("expected Run() to return an error for a failing graph")
	}

	last := readLastProgressEvent(t, filepath.Join(logsRoot, "progress.ndjson"))

	// The last line must be run_failed with correct fields.
	if last["event"] != "run_failed" {
		t.Fatalf("last progress event: got %q want %q", last["event"], "run_failed")
	}
	if last["status"] != "fail" {
		t.Fatalf("last event status: got %q want %q", last["status"], "fail")
	}
	if last["run_id"] != runID {
		t.Fatalf("last event run_id: got %q want %q", last["run_id"], runID)
	}
	ts, _ := last["ts"].(string)
	if strings.TrimSpace(ts) == "" {
		t.Fatal("last event ts is empty")
	}

	// final.json must exist when run_failed is observed (ordering guarantee).
	finalPath := filepath.Join(logsRoot, "final.json")
	if _, err := os.Stat(finalPath); err != nil {
		t.Fatalf("final.json must exist before run_failed is emitted: %v", err)
	}
}
