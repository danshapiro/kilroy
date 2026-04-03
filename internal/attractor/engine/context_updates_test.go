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

type setContextHandler struct{}

func (h *setContextHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	return runtime.Outcome{
		Status: runtime.StatusSuccess,
		ContextUpdates: map[string]any{
			"k": "v",
		},
	}, nil
}

func TestRun_ContextUpdatesAreMergedAndSavedInCheckpoint(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=diamond, type="setctx"]
  exit [shape=Msquare]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "ctx", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       []byte(""),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		AgentBackend: &SimulatedAgentBackend{},
	}
	eng.Registry.Register("setctx", &setContextHandler{})
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got := cp.ContextValues["k"]; got != "v" {
		t.Fatalf("checkpoint context k: got %v want %v", got, "v")
	}
}
