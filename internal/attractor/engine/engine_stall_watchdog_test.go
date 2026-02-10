package engine

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_StallWatchdog(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit [condition="outcome=success"]
}`)
	repo := initTestRepo(t)
	opts := RunOptions{
		RepoPath:           repo,
		StallTimeout:       150 * time.Millisecond,
		StallCheckInterval: 25 * time.Millisecond,
	}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stall watchdog timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stall watchdog") {
		t.Fatalf("expected stall watchdog error, got: %v", err)
	}
}

func TestRun_StallWatchdogInterruptsRetrySleep(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("requires false binary")
	}
	dot := []byte(`digraph G {
  graph [default_max_retry=5, retry.backoff.initial_delay_ms=5000, retry.backoff.backoff_factor=1, retry.backoff.max_delay_ms=5000]
  start [shape=Mdiamond]
  fail [shape=parallelogram, tool_command="false"]
  exit [shape=Msquare]
  start -> fail
  fail -> exit [condition="outcome=success"]
}`)
	repo := initTestRepo(t)
	opts := RunOptions{
		RepoPath:           repo,
		StallTimeout:       150 * time.Millisecond,
		StallCheckInterval: 25 * time.Millisecond,
	}

	start := time.Now()
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stall watchdog timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stall watchdog") {
		t.Fatalf("expected stall watchdog error, got: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("expected cancellation during retry sleep; elapsed=%s", elapsed)
	}
}

func TestRun_StallWatchdogStopsRunLoopBeforeFailEdgeTraversal(t *testing.T) {
	if _, err := exec.LookPath("false"); err != nil {
		t.Skip("requires false binary")
	}
	logsRoot := filepath.Join(t.TempDir(), "logs")
	dot := []byte(`digraph G {
  graph [default_max_retry=5, retry.backoff.initial_delay_ms=5000, retry.backoff.backoff_factor=1, retry.backoff.max_delay_ms=5000]
  start [shape=Mdiamond]
  fail [shape=parallelogram, tool_command="false"]
  after_fail [shape=parallelogram, tool_command="echo should-not-run"]
  exit [shape=Msquare]
  start -> fail
  fail -> after_fail [condition="outcome=fail"]
  after_fail -> exit [condition="outcome=success"]
}`)
	repo := initTestRepo(t)
	opts := RunOptions{
		RepoPath:           repo,
		LogsRoot:           logsRoot,
		StallTimeout:       150 * time.Millisecond,
		StallCheckInterval: 25 * time.Millisecond,
	}

	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stall watchdog timeout")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stall watchdog") {
		t.Fatalf("expected stall watchdog error, got: %v", err)
	}

	progressPath := filepath.Join(logsRoot, "progress.ndjson")
	if hasProgressEventForNode(t, progressPath, "stage_attempt_start", "after_fail") {
		t.Fatalf("run loop continued to fail edge node after cancellation: %s", progressPath)
	}
}
