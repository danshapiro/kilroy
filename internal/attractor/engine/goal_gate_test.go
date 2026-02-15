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

func TestRun_GoalGateEnforcedAtExit_RoutesToRetryTarget(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Tool node fails first time, succeeds second time using a marker file in the worktree.
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  gate [
    shape=parallelogram,
    goal_gate=true,
    retry_target=gate,
    max_retries=0,
    tool_command="test -f .attempt && echo ok || (touch .attempt; echo fail; exit 1)"
  ]
  start -> gate -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// gate/status.json should reflect the *final* (successful) attempt.
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "gate", "status.json"))
	if err != nil {
		t.Fatalf("read gate status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode gate status.json: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("gate outcome: got %q want %q", out.Status, runtime.StatusSuccess)
	}

	// Base + start + gate + gate + exit => 5 total commits.
	count := strings.TrimSpace(runCmdOut(t, repo, "git", "rev-list", "--count", res.RunBranch))
	if count != "5" {
		t.Fatalf("commit count: got %s want 5 (base+4 executed nodes)", count)
	}
}

func TestRun_GoalGateUnsatisfied_NoRetryTargetFails(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  gate [
    shape=parallelogram,
    goal_gate=true,
    tool_command="echo fail; exit 1"
  ]
  start -> gate -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "goal gate unsatisfied") {
		t.Fatalf("error: %v", err)
	}
}

