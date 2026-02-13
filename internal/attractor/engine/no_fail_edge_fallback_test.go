package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestRun_NoMatchingFailEdge_FallsBackToRetryTarget(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// "review" fails, has only a condition="outcome=yes" edge to exit.
	// No condition="outcome=fail" edge exists.
	// Graph-level retry_target points to "fix", which succeeds.
	// Engine should fall back to retry_target instead of dying.
	dot := []byte(`
digraph G {
  graph [goal="test", retry_target="fix"]
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  review [
    shape=parallelogram,
    tool_command="echo fail; exit 1"
  ]
  fix [
    shape=parallelogram,
    tool_command="echo fixed > fixed.txt"
  ]
  start -> review
  review -> exit [condition="outcome=yes"]
  review -> fix  [condition="outcome=__never__"]
  fix -> exit
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
}

func TestRun_NoMatchingFailEdge_NoRetryTarget_StillFails(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// No retry_target, no matching edge — should still fail.
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  review [
    shape=parallelogram,
    tool_command="echo fail; exit 1"
  ]
  start -> review
  review -> exit [condition="outcome=yes"]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "stage failed with no outgoing fail edge") {
		t.Fatalf("error: %v", err)
	}
}
