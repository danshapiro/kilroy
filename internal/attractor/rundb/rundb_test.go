// Tests for the run database: migration, CRUD, querying.
package rundb

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpen_CreatesDBAndMigrates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Verify the schema_migrations table exists and has version 1.
	var version int
	err = db.SQL().QueryRow("SELECT version FROM schema_migrations WHERE version = 1").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_migrations: %v", err)
	}
	if version != 1 {
		t.Fatalf("version = %d, want 1", version)
	}
}

func TestOpen_IdempotentMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db1, err := Open(path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	db1.Close()

	// Opening again should not fail (migrations already applied).
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	db2.Close()
}

func TestInsertRun_And_GetRun(t *testing.T) {
	db := openTestDB(t)

	err := db.InsertRun(RunRecord{
		RunID:     "run-001",
		GraphName: "test-graph",
		Goal:      "test goal",
		Status:    "running",
		LogsRoot:  "/tmp/logs/run-001",
		RepoPath:  "/tmp/repo",
		StartedAt: time.Now(),
		Labels:    map[string]string{"env": "test", "source": "ci"},
		Inputs:    map[string]any{"pr_number": 42},
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}

	run, err := db.GetRun("run-001")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run == nil {
		t.Fatal("GetRun returned nil")
	}
	if run.GraphName != "test-graph" {
		t.Fatalf("GraphName = %q, want %q", run.GraphName, "test-graph")
	}
	if run.Status != "running" {
		t.Fatalf("Status = %q, want %q", run.Status, "running")
	}
	if run.Labels["env"] != "test" {
		t.Fatalf("Labels[env] = %q, want %q", run.Labels["env"], "test")
	}
	if run.Labels["source"] != "ci" {
		t.Fatalf("Labels[source] = %q, want %q", run.Labels["source"], "ci")
	}
}

func TestCompleteRun(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{
		RunID:     "run-002",
		GraphName: "g",
		Status:    "running",
		StartedAt: time.Now().Add(-5 * time.Second),
	})

	err := db.CompleteRun("run-002", "success", "", "abc123", []string{"warn1"})
	if err != nil {
		t.Fatalf("CompleteRun: %v", err)
	}

	run, _ := db.GetRun("run-002")
	if run.Status != "success" {
		t.Fatalf("Status = %q, want %q", run.Status, "success")
	}
	if run.FinalSHA != "abc123" {
		t.Fatalf("FinalSHA = %q, want %q", run.FinalSHA, "abc123")
	}
	if run.CompletedAt == nil {
		t.Fatal("CompletedAt is nil")
	}
	if len(run.Warnings) != 1 || run.Warnings[0] != "warn1" {
		t.Fatalf("Warnings = %v, want [warn1]", run.Warnings)
	}
}

func TestNodeExecution_StartAndComplete(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "run-003", Status: "running", StartedAt: time.Now()})

	id, err := db.InsertNodeStart("run-003", "step_a", 1, "tool")
	if err != nil {
		t.Fatalf("InsertNodeStart: %v", err)
	}
	if id <= 0 {
		t.Fatalf("InsertNodeStart returned id=%d, want > 0", id)
	}

	err = db.CompleteNode(id, "success", "", "", "", "tool completed", nil)
	if err != nil {
		t.Fatalf("CompleteNode: %v", err)
	}

	nodes, err := db.GetNodeExecutions("run-003")
	if err != nil {
		t.Fatalf("GetNodeExecutions: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("len(nodes) = %d, want 1", len(nodes))
	}
	if nodes[0].NodeID != "step_a" {
		t.Fatalf("NodeID = %q, want %q", nodes[0].NodeID, "step_a")
	}
	if nodes[0].Status != "success" {
		t.Fatalf("Status = %q, want %q", nodes[0].Status, "success")
	}
}

func TestEdgeDecision(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "run-004", Status: "running", StartedAt: time.Now()})

	err := db.InsertEdgeDecision("run-004", "a", "b", "success", "outcome=success", "condition_match")
	if err != nil {
		t.Fatalf("InsertEdgeDecision: %v", err)
	}
}

func TestListRuns_FilterByStatus(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "r1", Status: "success", GraphName: "g1", StartedAt: time.Now().Add(-2 * time.Hour)})
	_ = db.InsertRun(RunRecord{RunID: "r2", Status: "fail", GraphName: "g2", StartedAt: time.Now().Add(-1 * time.Hour)})
	_ = db.InsertRun(RunRecord{RunID: "r3", Status: "success", GraphName: "g1", StartedAt: time.Now()})

	runs, err := db.ListRuns(ListFilter{Status: "success"})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("len = %d, want 2", len(runs))
	}
	// Newest first.
	if runs[0].RunID != "r3" {
		t.Fatalf("first run = %q, want r3", runs[0].RunID)
	}
}

func TestListRuns_FilterByLabel(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "r1", Status: "success", Labels: map[string]string{"env": "prod"}, StartedAt: time.Now()})
	_ = db.InsertRun(RunRecord{RunID: "r2", Status: "success", Labels: map[string]string{"env": "test"}, StartedAt: time.Now()})

	runs, err := db.ListRuns(ListFilter{Labels: map[string]string{"env": "prod"}})
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != "r1" {
		t.Fatalf("expected only r1, got %v", runs)
	}
}

func TestLatestRun(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "old", Status: "success", StartedAt: time.Now().Add(-1 * time.Hour)})
	_ = db.InsertRun(RunRecord{RunID: "new", Status: "running", StartedAt: time.Now()})

	run, err := db.LatestRun()
	if err != nil {
		t.Fatalf("LatestRun: %v", err)
	}
	if run.RunID != "new" {
		t.Fatalf("RunID = %q, want %q", run.RunID, "new")
	}
}

func TestPruneRuns_Before(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "old", Status: "success", StartedAt: time.Now().Add(-48 * time.Hour)})
	_ = db.InsertRun(RunRecord{RunID: "new", Status: "success", StartedAt: time.Now()})

	cutoff := time.Now().Add(-24 * time.Hour)
	n, err := db.PruneRuns(PruneFilter{Before: &cutoff})
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}

	runs, _ := db.ListRuns(ListFilter{})
	if len(runs) != 1 || runs[0].RunID != "new" {
		t.Fatalf("remaining runs: %v", runs)
	}
}

func TestPruneRuns_Orphans(t *testing.T) {
	db := openTestDB(t)
	existingDir := t.TempDir()
	_ = db.InsertRun(RunRecord{RunID: "exists", Status: "success", LogsRoot: existingDir, StartedAt: time.Now()})
	_ = db.InsertRun(RunRecord{RunID: "orphan", Status: "fail", LogsRoot: "/nonexistent/path/logs", StartedAt: time.Now()})

	n, err := db.PruneRuns(PruneFilter{Orphans: true})
	if err != nil {
		t.Fatalf("PruneRuns: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d, want 1", n)
	}

	run, _ := db.GetRun("exists")
	if run == nil {
		t.Fatal("existing run was pruned")
	}
	run, _ = db.GetRun("orphan")
	if run != nil {
		t.Fatal("orphan run was not pruned")
	}
}

func TestNodeCount_InRunSummary(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "r1", Status: "running", StartedAt: time.Now()})
	_, _ = db.InsertNodeStart("r1", "a", 1, "tool")
	_, _ = db.InsertNodeStart("r1", "b", 1, "tool")
	_, _ = db.InsertNodeStart("r1", "a", 2, "tool") // retry

	run, _ := db.GetRun("r1")
	if run.NodeCount != 3 {
		t.Fatalf("NodeCount = %d, want 3", run.NodeCount)
	}
}

func TestGetRun_NotFound(t *testing.T) {
	db := openTestDB(t)
	run, err := db.GetRun("nonexistent")
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if run != nil {
		t.Fatal("expected nil for nonexistent run")
	}
}

func TestDefaultPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/test-state")
	path := DefaultPath()
	want := "/tmp/test-state/kilroy/runs.db"
	if path != want {
		t.Fatalf("DefaultPath() = %q, want %q", path, want)
	}
}

func TestDefaultPath_FallsBackToHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/tmp/testhome")
	path := DefaultPath()
	if path == "" {
		t.Fatal("DefaultPath() is empty")
	}
	if !filepath.IsAbs(path) {
		t.Fatalf("DefaultPath() = %q, want absolute path", path)
	}
}

func TestCascadeDelete_NodesDeletedWithRun(t *testing.T) {
	db := openTestDB(t)
	_ = db.InsertRun(RunRecord{RunID: "r1", Status: "success", StartedAt: time.Now()})
	_, _ = db.InsertNodeStart("r1", "a", 1, "tool")
	_ = db.InsertEdgeDecision("r1", "start", "a", "", "", "lexical")

	// Delete the run.
	_, err := db.SQL().Exec("DELETE FROM runs WHERE run_id = 'r1'")
	if err != nil {
		t.Fatalf("delete run: %v", err)
	}

	nodes, _ := db.GetNodeExecutions("r1")
	if len(nodes) != 0 {
		t.Fatalf("expected 0 nodes after cascade delete, got %d", len(nodes))
	}
}

func init() {
	// Suppress unused import warning.
	_ = os.Stat
}
