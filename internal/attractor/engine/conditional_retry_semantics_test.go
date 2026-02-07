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

func TestRun_ConditionalNode_DoesNotConsumeRetryBudget(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test", default_max_retry=3]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  fail [shape=parallelogram, tool_command="echo nope >&2; exit 1"]
  cond [shape=diamond, label="Should not retry"]
  ok [shape=parallelogram, tool_command="echo ok > ok.txt"]

  start -> fail -> cond
  cond -> ok [condition="outcome=fail"]
  ok -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunID: "cond-retry", LogsRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Conditional stage reflects prior failure and does not get "max retries exceeded".
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "cond", "status.json"))
	if err != nil {
		t.Fatalf("read cond/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode cond/status.json: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Fatalf("cond status: got %q want %q (out=%+v)", out.Status, runtime.StatusFail, out)
	}
	if strings.TrimSpace(out.FailureReason) == "" {
		t.Fatalf("cond failure_reason should be non-empty (out=%+v)", out)
	}
	if strings.Contains(strings.ToLower(out.FailureReason), "max retries exceeded") {
		t.Fatalf("cond failure_reason should not be retry exhaustion (out=%+v)", out)
	}

	// Retry budget should not be spent on the conditional node.
	cp, err := runtime.LoadCheckpoint(filepath.Join(res.LogsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if v, ok := cp.NodeRetries["cond"]; ok && v > 0 {
		t.Fatalf("expected no retries for cond, got %d (node_retries=%v)", v, cp.NodeRetries)
	}
}
