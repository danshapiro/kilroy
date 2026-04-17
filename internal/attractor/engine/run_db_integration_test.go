// Integration test proving the engine records lifecycle events to the RunDB.
package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestRunDB_ToolGraphRecordsLifecycleEvents(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Open a test RunDB.
	dbPath := filepath.Join(t.TempDir(), "test-runs.db")
	rdb, err := rundb.Open(dbPath)
	if err != nil {
		t.Fatalf("Open rundb: %v", err)
	}
	defer rdb.Close()

	dot := []byte(`digraph rundb_test {
  graph [goal="Test RunDB lifecycle recording"]
  start [shape=Mdiamond]
  step_a [shape=parallelogram, tool_command="echo step_a_ok"]
  step_b [shape=parallelogram, tool_command="echo step_b_ok"]
  done [shape=Msquare]
  start -> step_a -> step_b -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "rundb-test-001",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
		RunDB:       rdb,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify run recorded.
	run, err := rdb.GetRun("rundb-test-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil {
		t.Fatal("run not recorded in DB")
	}
	if run.Status != "success" {
		t.Fatalf("run status = %q, want success", run.Status)
	}
	if run.GraphName != "rundb_test" {
		t.Fatalf("graph_name = %q, want rundb_test", run.GraphName)
	}

	// Verify node executions recorded.
	nodes, err := rdb.GetNodeExecutions("rundb-test-001")
	if err != nil {
		t.Fatalf("GetNodeExecutions: %v", err)
	}
	// start, step_a, step_b, done = 4 nodes
	if len(nodes) < 3 {
		t.Fatalf("expected at least 3 node executions, got %d", len(nodes))
	}

	// Verify edge decisions recorded.
	var edgeCount int
	err = rdb.SQL().QueryRow("SELECT COUNT(*) FROM edge_decisions WHERE run_id = ?", "rundb-test-001").Scan(&edgeCount)
	if err != nil {
		t.Fatalf("count edge decisions: %v", err)
	}
	// start→step_a, step_a→step_b, step_b→done = 3 edges
	if edgeCount < 3 {
		t.Fatalf("expected at least 3 edge decisions, got %d", edgeCount)
	}
}
