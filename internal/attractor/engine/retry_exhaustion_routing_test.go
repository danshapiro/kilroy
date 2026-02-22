package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestRun_RetryExhaustion_TracksRetriesAndRoutesUsingFinalFailOutcome(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  t [
    shape=parallelogram,
    max_retries=1,
    tool_command="echo attempt >> attempts.txt; exit 1"
  ]
  fail_route [
    shape=parallelogram,
    tool_command="echo fail > routed.txt"
  ]
  start -> t -> exit
  t -> fail_route [condition="outcome=fail"]
  fail_route -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunID: "retryexhaust", LogsRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Attempts are observable via a worktree file.
	attemptsBytes, err := os.ReadFile(filepath.Join(res.WorktreeDir, "attempts.txt"))
	if err != nil {
		t.Fatalf("read attempts.txt: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(attemptsBytes)), "\n")
	if got := len(lines); got != 2 {
		t.Fatalf("attempt count: got %d want %d", got, 2)
	}

	// Retry counter tracked per-node in checkpoint.
	cp, err := runtime.LoadCheckpoint(filepath.Join(res.LogsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got := cp.NodeRetries["t"]; got != 1 {
		t.Fatalf("checkpoint node_retries[t]: got %d want %d", got, 1)
	}

	// Final fail outcome drives failure edge selection (t -> fail_route).
	routedBytes, err := os.ReadFile(filepath.Join(res.WorktreeDir, "routed.txt"))
	if err != nil {
		t.Fatalf("read routed.txt: %v", err)
	}
	if got := strings.TrimSpace(string(routedBytes)); got != "fail" {
		t.Fatalf("routed.txt: got %q want %q", got, "fail")
	}
}
