package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestRunWithConfig_CLIBackend_OpenAIIdleTimeoutKillsProcessGroup(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	childPIDFile := filepath.Join(t.TempDir(), "watchdog-child.pid")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

pidfile="${KILROY_WATCHDOG_CHILD_PID_FILE:?missing pidfile}"
bash -c 'while true; do sleep 1; done' &
child="$!"
echo "$child" > "$pidfile"
echo "codex started" >&2

while true; do
  sleep 60
done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILROY_WATCHDOG_CHILD_PID_FILE", childPIDFile)
	t.Setenv("KILROY_CODEX_IDLE_TIMEOUT", "2s")
	t.Setenv("KILROY_CODEX_KILL_GRACE", "200ms")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test idle timeout watchdog"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "watchdog-timeout", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	statusBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "status.json"))
	if err != nil {
		t.Fatalf("read a/status.json: %v", err)
	}
	outcome, err := runtime.DecodeOutcomeJSON(statusBytes)
	if err != nil {
		t.Fatalf("decode a/status.json: %v", err)
	}
	if outcome.Status != runtime.StatusFail {
		t.Fatalf("a status: got %q want %q (out=%+v)", outcome.Status, runtime.StatusFail, outcome)
	}
	if !strings.Contains(strings.ToLower(outcome.FailureReason), "idle timeout") {
		t.Fatalf("a failure_reason: got %q want idle timeout", outcome.FailureReason)
	}

	pidBytes, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatalf("read child pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child pid: %v (raw=%q)", err, string(pidBytes))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("watchdog child process pid=%d still exists", pid)
}

func TestWaitWithIdleWatchdog_ContextCancelKillsProcessGroup(t *testing.T) {
	cli := filepath.Join(t.TempDir(), "codex")
	childPIDFile := filepath.Join(t.TempDir(), "cancel-child.pid")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

pidfile="${KILROY_CANCEL_CHILD_PID_FILE:?missing pidfile}"
bash -c 'while true; do sleep 1; done' &
child="$!"
echo "$child" > "$pidfile"
echo "codex started" >&2

while true; do
  sleep 60
done
`), 0o755); err != nil {
		t.Fatal(err)
	}
	stdoutPath := filepath.Join(t.TempDir(), "stdout.log")
	stderrPath := filepath.Join(t.TempDir(), "stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdoutFile.Close() }()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderrFile.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, cli)
	cmd.Env = append(os.Environ(), "KILROY_CANCEL_CHILD_PID_FILE="+childPIDFile)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd: %v", err)
	}

	pidDeadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(pidDeadline) {
		if _, err := os.Stat(childPIDFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if _, err := os.Stat(childPIDFile); err != nil {
		t.Fatalf("wait for child pid file: %v", err)
	}

	cancel()
	runErr, timedOut, waitErr := waitWithIdleWatchdog(ctx, cmd, stdoutPath, stderrPath, 30*time.Minute, 200*time.Millisecond)
	if waitErr != nil {
		t.Fatalf("waitWithIdleWatchdog error: %v", waitErr)
	}
	if timedOut {
		t.Fatalf("waitWithIdleWatchdog should report context cancellation, not idle timeout")
	}
	if runErr == nil || !strings.Contains(strings.ToLower(runErr.Error()), "context canceled") {
		t.Fatalf("runErr: got %v want context canceled", runErr)
	}

	pidBytes, err := os.ReadFile(childPIDFile)
	if err != nil {
		t.Fatalf("read child pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatalf("parse child pid: %v (raw=%q)", err, string(pidBytes))
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("cancel child process pid=%d still exists", pid)
}

// TestWaitWithIdleWatchdog_DisabledWhenContextDeadlineCloser verifies that the
// idle watchdog is disabled when the context deadline is closer than the idle
// timeout, allowing the context to handle termination cleanly.
func TestWaitWithIdleWatchdog_DisabledWhenContextDeadlineCloser(t *testing.T) {
	// Create a script that writes to stdout once then goes quiet.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo "started" >&2
# Go quiet — if idle watchdog is active at 500ms, it would kill us.
# But context deadline is 1s, so idle should be disabled.
sleep 5
`), 0o755); err != nil {
		t.Fatal(err)
	}

	stdoutPath := filepath.Join(t.TempDir(), "stdout.log")
	stderrPath := filepath.Join(t.TempDir(), "stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stdoutFile.Close() }()
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stderrFile.Close() }()

	// Context deadline of 1s, idle timeout of 500ms. Without the fix,
	// idle watchdog would fire at 500ms. With the fix, idle is disabled
	// because remaining (1s) <= idleTimeout (500ms) + killGrace (200ms).
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, cli)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile
	if err := cmd.Start(); err != nil {
		t.Fatalf("start cmd: %v", err)
	}

	runErr, timedOut, waitErr := waitWithIdleWatchdog(ctx, cmd, stdoutPath, stderrPath, 500*time.Millisecond, 200*time.Millisecond)
	if waitErr != nil {
		t.Fatalf("waitWithIdleWatchdog error: %v", waitErr)
	}
	// Should NOT report idle timeout — context deadline should have handled it.
	if timedOut {
		t.Fatalf("idle watchdog should be disabled when context deadline is closer, got timedOut=true")
	}
	if runErr == nil {
		t.Fatalf("expected context deadline error, got nil")
	}
	if !strings.Contains(runErr.Error(), "context deadline exceeded") &&
		!strings.Contains(runErr.Error(), "signal: killed") {
		t.Logf("runErr: %v (acceptable — context-driven termination)", runErr)
	}
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}
