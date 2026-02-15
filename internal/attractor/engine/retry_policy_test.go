package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestRun_RetriesOnFail_ThenSucceeds(t *testing.T) {
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
  t [
    shape=parallelogram,
    max_retries=1,
    tool_command="test -f .attempt && echo ok || (touch .attempt; echo fail; exit 1)"
  ]
  start -> t -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "t", "status.json"))
	if err != nil {
		t.Fatalf("read t status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode t status.json: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("t outcome: got %q want %q", out.Status, runtime.StatusSuccess)
	}
}

// TestRun_ExplicitMaxRetriesZero_NoRetries verifies that a node with
// max_retries=0 is NOT overridden by the graph default_max_retry. This
// tests the fix for the sentinel bug where explicit zero was conflated
// with "not set" (V2.2 in the spec compliance audit).
func TestRun_ExplicitMaxRetriesZero_NoRetries(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Node t has max_retries=0 (explicit). Graph default_max_retry=5.
	// The node should execute exactly once and fail (no retries),
	// even though the graph default is 5.
	dot := []byte(`
digraph G {
  graph [goal="test", default_max_retry=5]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [
    shape=parallelogram,
    max_retries=0,
    tool_command="echo fail; exit 1"
  ]
  start -> t -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "t", "status.json"))
	if err != nil {
		t.Fatalf("read t status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode t status.json: %v", err)
	}
	// Node with max_retries=0 should fail after exactly 1 attempt.
	if out.Status != runtime.StatusFail {
		t.Fatalf("t outcome: got %q want %q (explicit max_retries=0 should mean no retries)", out.Status, runtime.StatusFail)
	}
}

// TestRun_DefaultMaxRetryFallback verifies the retry precedence chain:
// (1) node max_retries, (2) graph default_max_retry, (3) built-in default of 3.
func TestRun_DefaultMaxRetryFallback(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// No default_max_retry on graph, no max_retries on node.
	// Built-in default is 3, so node should retry up to 3 times (4 attempts total).
	// Tool fails on attempts 1-3, succeeds on attempt 4 (uses counter file).
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [
    shape=parallelogram,
    tool_command="n=$(cat .counter 2>/dev/null || echo 0); n=$((n+1)); echo $n > .counter; test $n -ge 4 && echo ok || (echo fail; exit 1)"
  ]
  start -> t -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "t", "status.json"))
	if err != nil {
		t.Fatalf("read t status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode t status.json: %v", err)
	}
	// With built-in default of 3 retries (4 attempts), attempt 4 should succeed.
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("t outcome: got %q want %q (built-in default of 3 retries should allow 4 attempts)", out.Status, runtime.StatusSuccess)
	}
}

func TestRun_AllowPartialAfterRetryExhaustion(t *testing.T) {
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
  t [
    shape=parallelogram,
    max_retries=1,
    allow_partial=true,
    tool_command="echo fail; exit 1"
  ]
  start -> t -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "t", "status.json"))
	if err != nil {
		t.Fatalf("read t status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode t status.json: %v", err)
	}
	if out.Status != runtime.StatusPartialSuccess {
		t.Fatalf("t outcome: got %q want %q", out.Status, runtime.StatusPartialSuccess)
	}
}

