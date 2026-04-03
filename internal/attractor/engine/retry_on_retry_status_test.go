package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type retryThenSuccessHandler struct{}

func (h *retryThenSuccessHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)

	// Use marker files to make the attempts observable in a spec-driven way.
	a1 := filepath.Join(stageDir, "attempt_1")
	a2 := filepath.Join(stageDir, "attempt_2")
	if _, err := os.Stat(a1); err != nil {
		_ = os.WriteFile(a1, []byte("1"), 0o644)
		return runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "try again"}, nil
	}
	_ = os.WriteFile(a2, []byte("2"), 0o644)
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "ok"}, nil
}

func TestRun_RetriesOnRetryStatus(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  r [shape=diamond, type="retry_then_success", max_retries=1]
  exit [shape=Msquare]
  start -> r -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	opts := RunOptions{RepoPath: repo, RunID: "retry", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		AgentBackend: &SimulatedAgentBackend{},
	}
	eng.Registry.Register("retry_then_success", &retryThenSuccessHandler{})
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	assertExists(t, filepath.Join(logsRoot, "r", "attempt_1"))
	assertExists(t, filepath.Join(logsRoot, "r", "attempt_2"))
	b, err := os.ReadFile(filepath.Join(logsRoot, "r", "status.json"))
	if err != nil {
		t.Fatalf("read status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("final status: got %q want %q", out.Status, runtime.StatusSuccess)
	}
}
