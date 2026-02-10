package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRun_DeterministicFailureCycle_AbortsInfiniteLoop verifies that when
// every stage in a retry cycle fails with a deterministic failure (e.g.,
// expired auth token), the engine aborts the run instead of looping forever.
//
// The graph has a cycle: implement -> verify -> check -> implement (on fail).
// All tool nodes exit 1 to simulate a persistent provider failure.
// The engine should detect the repeated failure signature and terminate.
func TestRun_DeterministicFailureCycle_AbortsInfiniteLoop(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [default_max_retry=0]
  start [shape=Mdiamond]
  exit [shape=Msquare]

  implement [
    shape=parallelogram,
    tool_command="echo implement_fail >> log.txt; exit 1"
  ]
  verify [
    shape=parallelogram,
    tool_command="echo verify_fail >> log.txt; exit 1"
  ]
  check [shape=diamond]

  start -> implement
  implement -> verify
  verify -> check
  check -> implement [condition="outcome=fail", label="retry"]
  check -> exit [condition="outcome=success"]
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunID: "detfailcycle", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected run to abort with deterministic failure cycle error, but it succeeded")
	}
	if !strings.Contains(err.Error(), "deterministic failure cycle") {
		t.Fatalf("expected deterministic failure cycle error, got: %v", err)
	}
}

// TestRun_DeterministicFailure_SingleRouteToRecovery_StillWorks verifies
// that a single deterministic failure that routes to a recovery node (not a
// cycle) still works correctly — we don't want the cycle breaker to be too
// aggressive and block legitimate fail-routing.
func TestRun_DeterministicFailure_SingleRouteToRecovery_StillWorks(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [default_max_retry=0]
  start [shape=Mdiamond]
  exit [shape=Msquare]

  attempt [
    shape=parallelogram,
    tool_command="exit 1"
  ]
  recovery [
    shape=parallelogram,
    tool_command="echo recovered > result.txt"
  ]

  start -> attempt -> exit
  attempt -> recovery [condition="outcome=fail"]
  recovery -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunID: "detfailrecovery", LogsRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	resultBytes, err := os.ReadFile(filepath.Join(res.WorktreeDir, "result.txt"))
	if err != nil {
		t.Fatalf("read result.txt: %v", err)
	}
	if got := strings.TrimSpace(string(resultBytes)); got != "recovered" {
		t.Fatalf("result.txt: got %q want %q", got, "recovered")
	}
}

func TestRunSubgraphUntil_DeterministicFailureCycleBreaksAtLimit(t *testing.T) {
	err := runDeterministicSubgraphCycleFixture(t, 2)
	if err == nil || !strings.Contains(err.Error(), "deterministic failure cycle") {
		t.Fatalf("expected deterministic failure cycle error, got %v", err)
	}
}

func TestDeterministicFailureCycleBreaker_IgnoresCanceledClass(t *testing.T) {
	err := runCanceledCycleFixture(t)
	if err != nil && strings.Contains(err.Error(), "deterministic failure cycle") {
		t.Fatalf("canceled failures should not trip deterministic cycle breaker: %v", err)
	}
}
