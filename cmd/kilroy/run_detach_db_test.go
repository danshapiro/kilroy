package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

// TestRegisterDetachedRunInDB_AppearsBefore_TerminalState verifies the key
// invariant of the detach-registration fix: after registerDetachedRunInDB
// returns (and before the child process calls RecordRunStart), the run row
// exists in the DB with status=running and is discoverable via ListRuns with
// label filters — exactly the query used by `runs wait --latest --label ...`.
func TestRegisterDetachedRunInDB_AppearsBefore_TerminalState(t *testing.T) {
	// Redirect the DB to a temp directory so we don't pollute the real DB.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Write a minimal DOT graph file.
	graphDir := t.TempDir()
	graphFile := filepath.Join(graphDir, "test_detach.dot")
	dotContent := []byte(`digraph test_detach_graph {
  graph [goal="Test detach registration"]
  start [shape=Mdiamond]
  done [shape=Msquare]
  start -> done
}`)
	if err := os.WriteFile(graphFile, dotContent, 0o644); err != nil {
		t.Fatalf("write graph file: %v", err)
	}

	const runID = "detach-register-test-001"
	logsRoot := t.TempDir()
	labels := map[string]string{"env": "test", "task": "detach-db-test"}
	invocation := []string{"kilroy", "attractor", "run", "--detach", "--graph", graphFile, "--label", "env=test"}

	// This is the call the parent makes before forking the child.
	registerDetachedRunInDB(runID, graphFile, logsRoot, "/tmp/repo", labels, nil, invocation)

	// Verify the run appears in the DB.
	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		t.Fatalf("open rundb: %v", err)
	}
	defer db.Close()

	// GetRun must return the row.
	run, err := db.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil {
		t.Fatal("run not found in DB: GetRun returned nil (bug: detached runs not pre-registered)")
	}

	// Status must be running (not yet terminal).
	if run.Status != "running" {
		t.Errorf("status = %q, want \"running\"", run.Status)
	}
	// Graph name parsed from DOT source.
	if run.GraphName != "test_detach_graph" {
		t.Errorf("graph_name = %q, want \"test_detach_graph\"", run.GraphName)
	}
	// Labels populated.
	if run.Labels["env"] != "test" {
		t.Errorf("labels[env] = %q, want \"test\"", run.Labels["env"])
	}
	if run.Labels["task"] != "detach-db-test" {
		t.Errorf("labels[task] = %q, want \"detach-db-test\"", run.Labels["task"])
	}
	// LogsRoot set.
	if run.LogsRoot != logsRoot {
		t.Errorf("logs_root = %q, want %q", run.LogsRoot, logsRoot)
	}
	// started_at is a recent timestamp.
	if run.StartedAt.IsZero() || run.StartedAt.After(time.Now().Add(time.Second)) {
		t.Errorf("unexpected started_at: %v", run.StartedAt)
	}

	// ListRuns with label filter — this is what `runs wait --latest --label env=test` uses.
	runs, err := db.ListRuns(rundb.ListFilter{Labels: map[string]string{"task": "detach-db-test"}, Limit: 1})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) == 0 {
		t.Fatal("ListRuns: run not found by label filter (bug: detached runs not pre-registered)")
	}
	if runs[0].RunID != runID {
		t.Errorf("ListRuns[0].RunID = %q, want %q", runs[0].RunID, runID)
	}
	if runs[0].Status != "running" {
		t.Errorf("ListRuns[0].Status = %q, want \"running\"", runs[0].Status)
	}

	// Simulate the engine completing the run (what the child does at the end).
	// The child first calls INSERT OR REPLACE (which is still status=running),
	// then calls CompleteRun. Verify the status transitions correctly.
	if err := db.CompleteRun(runID, "success", "", "", nil); err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}
	completed, err := db.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun after complete: %v", err)
	}
	if completed == nil {
		t.Fatal("run disappeared after CompleteRun")
	}
	if completed.Status != "success" {
		t.Errorf("post-complete status = %q, want \"success\"", completed.Status)
	}
	if completed.CompletedAt == nil {
		t.Error("completed_at is nil after CompleteRun")
	}
}

// TestRegisterDetachedRunInDB_FallsBackToFilename verifies that when the graph
// file cannot be parsed (or is absent), registerDetachedRunInDB still writes the
// row using the filename stem as a fallback graph name.
func TestRegisterDetachedRunInDB_FallsBackToFilename(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	// Point to a non-existent graph file.
	const runID = "detach-fallback-test-001"
	logsRoot := t.TempDir()
	labels := map[string]string{"test": "fallback"}

	registerDetachedRunInDB(runID, "/nonexistent/path/my_graph.dot", logsRoot, "", labels, nil, nil)

	db, err := rundb.Open(rundb.DefaultPath())
	if err != nil {
		t.Fatalf("open rundb: %v", err)
	}
	defer db.Close()

	run, err := db.GetRun(runID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil {
		t.Fatal("run not found in DB: fallback path failed")
	}
	if run.Status != "running" {
		t.Errorf("status = %q, want \"running\"", run.Status)
	}
	// Filename stem used when DOT parsing fails.
	if run.GraphName != "my_graph" {
		t.Errorf("graph_name = %q, want \"my_graph\"", run.GraphName)
	}
}
