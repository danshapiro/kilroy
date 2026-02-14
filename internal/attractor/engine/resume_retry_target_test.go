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

// TestResume_NoMatchingFailEdge_FallsBackToRetryTarget verifies that the resume
// path mirrors the forward path's retry_target fallback when a failed node has
// no outgoing fail edge.
func TestResume_NoMatchingFailEdge_FallsBackToRetryTarget(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// "review" fails (exit 1), has only condition="outcome=yes" edge → no matching fail edge.
	// Graph-level retry_target points to "fix", which succeeds.
	// Forward path falls back to retry_target; resume must do the same.
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

	// Run to completion first so we have valid artifacts/checkpoint.
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Find the git SHA for the "review" node commit.
	log := runCmdOut(t, repo, "git", "log", "--format=%H:%s", res.RunBranch)
	reviewSHA := ""
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		msg := strings.TrimSpace(parts[1])
		if strings.Contains(msg, "review (") {
			reviewSHA = strings.TrimSpace(parts[0])
			break
		}
	}
	if reviewSHA == "" {
		t.Fatalf("could not find commit for node review in log:\n%s", log)
	}

	// Rewrite checkpoint to simulate a crash right after "review" completed (failed).
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "review"
	cp.CompletedNodes = []string{"start", "review"}
	cp.GitCommitSHA = reviewSHA
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	// Resume should use retry_target fallback to "fix" and succeed.
	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	if res2.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res2.FinalStatus, runtime.FinalSuccess)
	}
}
