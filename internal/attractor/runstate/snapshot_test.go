package runstate

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestLoadSnapshot_FinalStateWinsAndIgnoresLiveForStateAndNode(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "final.json"), []byte(`{"status":"success","run_id":"r1"}`), 0o644)
	_ = os.WriteFile(filepath.Join(root, "live.json"), []byte(`{"event":"llm_retry","node_id":"impl"}`), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if s.State != StateSuccess {
		t.Fatalf("state=%q want %q", s.State, StateSuccess)
	}
	if s.RunID != "r1" {
		t.Fatalf("run_id=%q want r1", s.RunID)
	}
	if s.CurrentNodeID != "" {
		t.Fatalf("current_node_id=%q want empty when final.json is present", s.CurrentNodeID)
	}
	if s.LastEvent != "" {
		t.Fatalf("last_event=%q want empty when final.json is present", s.LastEvent)
	}
}

func TestLoadSnapshot_InfersRunningFromAlivePID(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "run.pid"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if !s.PIDAlive {
		t.Fatal("expected pid to be alive")
	}
	if s.State != StateRunning {
		t.Fatalf("state=%q want %q", s.State, StateRunning)
	}
}

func TestLoadSnapshot_NilEventFieldsDoNotRenderAsNilString(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "live.json"), []byte(`{"event":null,"node_id":null}`), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if s.LastEvent != "" || s.CurrentNodeID != "" {
		t.Fatalf("expected empty strings, got event=%q node=%q", s.LastEvent, s.CurrentNodeID)
	}
}

func TestApplyVerbose_PopulatesAllFields(t *testing.T) {
	root := t.TempDir()

	// checkpoint.json
	_ = os.WriteFile(filepath.Join(root, "checkpoint.json"),
		[]byte(`{"completed_nodes":["start","implement"],"node_retries":{"implement":2}}`), 0o644)

	// final.json with verbose fields
	_ = os.WriteFile(filepath.Join(root, "final.json"),
		[]byte(`{"status":"success","run_id":"r1","final_git_commit_sha":"abc123","cxdb_context_id":"42"}`), 0o644)

	// progress.ndjson with stage and edge events
	ndjson := `{"event":"stage_attempt_end","node_id":"start","status":"success","attempt":1,"max":4}
{"event":"edge_selected","from_node":"start","to_node":"implement","condition":"outcome=success"}
{"event":"stage_attempt_end","node_id":"implement","status":"fail","attempt":1,"max":4,"failure_reason":"exit status 1"}
`
	_ = os.WriteFile(filepath.Join(root, "progress.ndjson"), []byte(ndjson), 0o644)

	// worktree artifacts
	aiDir := filepath.Join(root, "worktree", ".ai")
	_ = os.MkdirAll(aiDir, 0o755)
	_ = os.WriteFile(filepath.Join(aiDir, "postmortem_latest.md"), []byte("# Postmortem\nfailed"), 0o644)
	_ = os.WriteFile(filepath.Join(aiDir, "review_final.md"), []byte("# Review\nlgtm"), 0o644)

	s := &Snapshot{LogsRoot: root}
	if err := ApplyVerbose(s); err != nil {
		t.Fatalf("ApplyVerbose: %v", err)
	}

	// checkpoint
	if len(s.CompletedNodes) != 2 || s.CompletedNodes[0] != "start" {
		t.Fatalf("completed_nodes=%v", s.CompletedNodes)
	}
	if s.RetryCounts["implement"] != 2 {
		t.Fatalf("retry_counts=%v", s.RetryCounts)
	}

	// final
	if s.FinalCommitSHA != "abc123" {
		t.Fatalf("final_commit_sha=%q", s.FinalCommitSHA)
	}
	if s.CXDBContextID != "42" {
		t.Fatalf("cxdb_context_id=%q", s.CXDBContextID)
	}

	// stage trace
	if len(s.StageTrace) != 2 {
		t.Fatalf("stage_trace len=%d want 2", len(s.StageTrace))
	}
	if s.StageTrace[0].NodeID != "start" || s.StageTrace[0].Status != "success" {
		t.Fatalf("stage_trace[0]=%+v", s.StageTrace[0])
	}
	if s.StageTrace[1].FailureReason != "exit status 1" {
		t.Fatalf("stage_trace[1]=%+v", s.StageTrace[1])
	}

	// edge trace
	if len(s.EdgeTrace) != 1 || s.EdgeTrace[0].From != "start" || s.EdgeTrace[0].To != "implement" {
		t.Fatalf("edge_trace=%+v", s.EdgeTrace)
	}

	// worktree artifacts
	if s.PostmortemText != "# Postmortem\nfailed" {
		t.Fatalf("postmortem=%q", s.PostmortemText)
	}
	if s.ReviewText != "# Review\nlgtm" {
		t.Fatalf("review=%q", s.ReviewText)
	}
}

func TestApplyVerbose_MissingFilesAreSkipped(t *testing.T) {
	root := t.TempDir()
	s := &Snapshot{LogsRoot: root}
	if err := ApplyVerbose(s); err != nil {
		t.Fatalf("ApplyVerbose on empty dir: %v", err)
	}
	if len(s.StageTrace) != 0 || len(s.CompletedNodes) != 0 || s.FinalCommitSHA != "" {
		t.Fatal("expected all verbose fields empty for missing files")
	}
}

func TestLoadSnapshot_TerminalStateIgnoresMalformedPIDFile(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "final.json"), []byte(`{"status":"success","run_id":"r1"}`), 0o644)
	_ = os.WriteFile(filepath.Join(root, "run.pid"), []byte("not-a-number"), 0o644)

	s, err := LoadSnapshot(root)
	if err != nil {
		t.Fatalf("LoadSnapshot: %v", err)
	}
	if s.State != StateSuccess {
		t.Fatalf("state=%q want %q", s.State, StateSuccess)
	}
	if s.PID != 0 {
		t.Fatalf("pid=%d want 0 for malformed pid file", s.PID)
	}
	if s.PIDAlive {
		t.Fatal("pid_alive=true want false for malformed pid file")
	}
}
