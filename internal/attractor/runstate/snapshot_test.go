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
