package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// --- Unit tests for archivePriorVisitDir ---

func TestArchivePriorVisitDir_MovesFilesToVisit1(t *testing.T) {
	stageDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(stageDir, "response.md"), []byte("visit 1 response"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "status.json"), []byte(`{"status":"success"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	archivePriorVisitDir(stageDir)

	visit1 := filepath.Join(stageDir, "visit_1")
	if _, err := os.Stat(visit1); err != nil {
		t.Fatalf("visit_1 dir not created: %v", err)
	}
	for _, name := range []string{"response.md", "status.json"} {
		data, err := os.ReadFile(filepath.Join(visit1, name))
		if err != nil {
			t.Fatalf("visit_1/%s not present: %v", name, err)
		}
		// Content must be preserved.
		if name == "response.md" && string(data) != "visit 1 response" {
			t.Fatalf("visit_1/response.md content: got %q", data)
		}
		// Original flat files must be gone (moved, not copied).
		if _, err := os.Stat(filepath.Join(stageDir, name)); err == nil {
			t.Fatalf("flat %s still exists after archive — expected move not copy", name)
		}
	}
}

func TestArchivePriorVisitDir_SequentialVisitsNumberCorrectly(t *testing.T) {
	stageDir := t.TempDir()

	// Simulate first archive (visit_1 already exists from prior call).
	_ = os.MkdirAll(filepath.Join(stageDir, "visit_1"), 0o755)
	_ = os.WriteFile(filepath.Join(stageDir, "visit_1", "response.md"), []byte("visit 1"), 0o644)

	// Now there's new content for visit 2.
	if err := os.WriteFile(filepath.Join(stageDir, "response.md"), []byte("visit 2 response"), 0o644); err != nil {
		t.Fatal(err)
	}

	archivePriorVisitDir(stageDir)

	data, err := os.ReadFile(filepath.Join(stageDir, "visit_2", "response.md"))
	if err != nil {
		t.Fatalf("visit_2/response.md not created: %v", err)
	}
	if string(data) != "visit 2 response" {
		t.Fatalf("visit_2/response.md: got %q want %q", data, "visit 2 response")
	}
	// visit_1 must be untouched.
	if d, _ := os.ReadFile(filepath.Join(stageDir, "visit_1", "response.md")); string(d) != "visit 1" {
		t.Fatalf("visit_1/response.md modified: got %q", d)
	}
}

func TestArchivePriorVisitDir_EmptyDirIsNoOp(t *testing.T) {
	stageDir := t.TempDir()
	archivePriorVisitDir(stageDir) // should not create visit_1

	entries, _ := os.ReadDir(stageDir)
	if len(entries) != 0 {
		t.Fatalf("expected empty dir after no-op, got %d entries", len(entries))
	}
}

func TestArchivePriorVisitDir_NonExistentDirIsNoOp(t *testing.T) {
	// Should not panic when stageDir doesn't exist yet.
	archivePriorVisitDir("/tmp/kilroy-test-does-not-exist-xyz987")
}

func TestArchivePriorVisitDir_OnlyVisitDirsIsNoOp(t *testing.T) {
	stageDir := t.TempDir()
	// Only visit_1 exists — nothing new to archive.
	_ = os.MkdirAll(filepath.Join(stageDir, "visit_1"), 0o755)
	_ = os.WriteFile(filepath.Join(stageDir, "visit_1", "response.md"), []byte("prior"), 0o644)

	archivePriorVisitDir(stageDir)

	entries, _ := os.ReadDir(stageDir)
	if len(entries) != 1 || entries[0].Name() != "visit_1" {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("expected only visit_1, got %v", names)
	}
}

func TestArchivePriorVisitDir_AttemptSubdirsAreMoved(t *testing.T) {
	// When a node has attempt_1/ dirs from within-loop retries, those subdirs
	// should also be moved into visit_N/ so the full retry history is preserved.
	stageDir := t.TempDir()
	_ = os.MkdirAll(filepath.Join(stageDir, "attempt_1"), 0o755)
	_ = os.WriteFile(filepath.Join(stageDir, "attempt_1", "response.md"), []byte("retry 1"), 0o644)
	_ = os.WriteFile(filepath.Join(stageDir, "response.md"), []byte("final"), 0o644)

	archivePriorVisitDir(stageDir)

	// Both attempt_1/ and response.md should be under visit_1/.
	if _, err := os.Stat(filepath.Join(stageDir, "visit_1", "attempt_1")); err != nil {
		t.Fatalf("visit_1/attempt_1 not moved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stageDir, "visit_1", "response.md")); err != nil {
		t.Fatalf("visit_1/response.md not moved: %v", err)
	}
	// Flat files and attempt subdirs must be gone from stageDir.
	if _, err := os.Stat(filepath.Join(stageDir, "response.md")); err == nil {
		t.Fatal("flat response.md still exists after move")
	}
	if _, err := os.Stat(filepath.Join(stageDir, "attempt_1")); err == nil {
		t.Fatal("attempt_1/ still exists after move")
	}
}

// --- Integration test: engine archives prior visit on node re-entry ---

// visitTrackingHandler writes response.md with the global call count so we can
// tell which visit produced which output. It returns success on every call.
type visitTrackingHandler struct {
	callCount int
}

func (h *visitTrackingHandler) Execute(_ context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	h.callCount++
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)
	_ = os.WriteFile(filepath.Join(stageDir, "response.md"),
		[]byte(fmt.Sprintf("response from visit %d", h.callCount)), 0o644)
	return runtime.Outcome{Status: runtime.StatusSuccess}, nil
}

// failThenSucceedHandler fails on the first call (emitting a retry outcome) and
// succeeds on the second so the retry_target path can route back to the earlier node.
type failThenSucceedHandler struct {
	callCount int
}

func (h *failThenSucceedHandler) Execute(_ context.Context, _ *Execution, _ *model.Node) (runtime.Outcome, error) {
	h.callCount++
	if h.callCount == 1 {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "injected failure"}, nil
	}
	return runtime.Outcome{Status: runtime.StatusSuccess}, nil
}

// TestRun_PriorVisitArchivedOnReentry verifies that when the engine routes back
// to a previously-visited node (via retry_target), the node's prior output files
// (response.md, status.json) are moved into visit_1/ before the second execution,
// so neither visit's artifacts are silently lost.
func TestRun_PriorVisitArchivedOnReentry(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()

	// Graph: start -> work -> verify -> exit (success) or verify -> work (fail fallback).
	// verify returns fail on its first call (routing unconditionally back to work)
	// and success on its second call (routing to exit). This causes work to be
	// visited twice, which is exactly when archivePriorVisitDir must preserve
	// the first visit's files.
	g, _, err := Prepare([]byte(`
digraph G {
  start   [shape=Mdiamond]
  work    [shape=diamond, type="visit_tracking"]
  verify  [shape=diamond, type="fail_then_succeed"]
  exit    [shape=Msquare]
  start -> work -> verify
  verify -> exit [condition="outcome=success"]
  verify -> work
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	workHandler := &visitTrackingHandler{}
	verifyHandler := &failThenSucceedHandler{}

	opts := RunOptions{RepoPath: repo, RunID: "visitarchivetest", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:        g,
		Options:      opts,
		DotSource:    []byte(""),
		LogsRoot:     opts.LogsRoot,
		WorktreeDir:  opts.WorktreeDir,
		Context:      runtime.NewContext(),
		Registry:     NewDefaultRegistry(),
		Interviewer:  &AutoApproveInterviewer{},
		AgentBackend: &SimulatedAgentBackend{},
	}
	eng.Registry.Register("visit_tracking", workHandler)
	eng.Registry.Register("fail_then_succeed", verifyHandler)
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	workDir := filepath.Join(logsRoot, "work")

	// work was called twice.
	if workHandler.callCount != 2 {
		t.Fatalf("expected work visited 2 times, got %d", workHandler.callCount)
	}

	// visit_1/ must exist and contain the first visit's response.
	visit1Response, err := os.ReadFile(filepath.Join(workDir, "visit_1", "response.md"))
	if err != nil {
		t.Fatalf("visit_1/response.md: %v", err)
	}
	if string(visit1Response) != "response from visit 1" {
		t.Fatalf("visit_1/response.md: got %q want %q", visit1Response, "response from visit 1")
	}

	// Flat response.md must be the second visit's content.
	flatResponse, err := os.ReadFile(filepath.Join(workDir, "response.md"))
	if err != nil {
		t.Fatalf("flat response.md: %v", err)
	}
	if string(flatResponse) != "response from visit 2" {
		t.Fatalf("flat response.md: got %q want %q", flatResponse, "response from visit 2")
	}

	// visit_2/ must NOT exist — second visit is the final, stays in flat dir.
	if _, err := os.Stat(filepath.Join(workDir, "visit_2")); err == nil {
		t.Fatal("visit_2/ should not exist; second visit stays in flat dir")
	}
}
