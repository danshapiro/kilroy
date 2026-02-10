package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/procutil"
)

func requireProcFS(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/proc/self/stat"); err != nil {
		t.Skip("requires procfs")
	}
}

func TestAttractorStop_KillsVerifiedAttractorProcessFromRunPID(t *testing.T) {
	requireProcFS(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	cxdb := newCXDBTestServer(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdb.URL(), cxdb.BinaryAddr(), catalog)
	graph := writeStopGraph(t)
	logs := filepath.Join(t.TempDir(), "logs")

	runCmd := exec.Command(
		bin,
		"attractor", "run",
		"--detach",
		"--graph", graph,
		"--config", cfg,
		"--run-id", "stop-smoke",
		"--logs-root", logs,
	)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detached run launch failed: %v\n%s", err, runOut)
	}

	pidPath := filepath.Join(logs, "run.pid")
	waitForFile(t, pidPath, 5*time.Second)
	pid := readPIDFile(t, pidPath)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs, "--grace-ms", "500", "--force").CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stopped=") {
		t.Fatalf("unexpected output: %s", out)
	}
	waitForProcessExit(t, pid, 10*time.Second)
}

func TestAttractorStop_PrefersRunIDOverRelativeLogsRootMismatch(t *testing.T) {
	requireProcFS(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	cxdb := newCXDBTestServer(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdb.URL(), cxdb.BinaryAddr(), catalog)
	graph := writeStopGraph(t)

	logsRoot := t.TempDir()
	realLogs := filepath.Join(logsRoot, "logs-real")
	if err := os.MkdirAll(realLogs, 0o755); err != nil {
		t.Fatalf("mkdir real logs: %v", err)
	}
	linkLogs := filepath.Join(logsRoot, "logs-link")
	if err := os.Symlink(realLogs, linkLogs); err != nil {
		t.Fatalf("symlink logs root: %v", err)
	}
	runID := "run-id-precedence"

	runCmd := exec.Command(
		bin,
		"attractor", "run",
		"--detach",
		"--graph", graph,
		"--config", cfg,
		"--run-id", runID,
		"--logs-root", linkLogs,
	)
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("detached run launch failed: %v\n%s", err, runOut)
	}

	waitForFile(t, filepath.Join(realLogs, "run.pid"), 5*time.Second)
	waitForFile(t, filepath.Join(realLogs, "manifest.json"), 5*time.Second)
	pid := readPIDFile(t, filepath.Join(realLogs, "run.pid"))

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", realLogs, "--grace-ms", "500", "--force").CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stopped=") {
		t.Fatalf("unexpected output: %s", out)
	}
	waitForProcessExit(t, pid, 10*time.Second)
}

func TestAttractorStop_RefusesAttractorProcessWithoutIdentityFlags(t *testing.T) {
	requireProcFS(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	cxdb := newCXDBTestServer(t)
	repo := initTestRepo(t)
	catalog := writePinnedCatalog(t)
	cfg := writeRunConfig(t, repo, cxdb.URL(), cxdb.BinaryAddr(), catalog)
	graph := writeStopGraph(t)

	proc := exec.Command(bin, "attractor", "run", "--graph", graph, "--config", cfg)
	if err := proc.Start(); err != nil {
		t.Fatalf("start attractor run process: %v", err)
	}
	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
		}
	})
	pid := proc.Process.Pid
	if pid <= 0 {
		t.Fatalf("invalid pid: %d", pid)
	}
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(pid)), 0o644)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs).CombinedOutput()
	if err == nil {
		t.Fatalf("expected stop to fail when identity flags are missing; output=%s", out)
	}
	if !strings.Contains(string(out), "no --logs-root/--run-id") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStop_RefusesPIDWithoutAttractorIdentity(t *testing.T) {
	requireProcFS(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()

	proc := exec.Command("sleep", "60")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
		}
	})
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0o644)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs).CombinedOutput()
	if err == nil {
		t.Fatalf("expected stop to fail for non-attractor pid; output=%s", out)
	}
	if !strings.Contains(string(out), "refusing to signal pid") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStop_RefusesWhenRunIsTerminal(t *testing.T) {
	requireProcFS(t)
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()

	proc := exec.Command("sleep", "60")
	if err := proc.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		if proc.Process != nil {
			_ = proc.Process.Kill()
		}
	})
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success","run_id":"r1"}`), 0o644)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs).CombinedOutput()
	if err == nil {
		t.Fatalf("expected stop to fail for terminal run; output=%s", out)
	}
	if !strings.Contains(string(out), `run state is "success"`) {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStop_ErrorsWhenNoPID(t *testing.T) {
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs).CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output=%s", out)
	}
}

func TestVerifyProcessIdentity_DetectsChangedStartTime(t *testing.T) {
	requireProcFS(t)
	start, err := procutil.ReadPIDStartTime(os.Getpid())
	if err != nil {
		t.Fatalf("ReadPIDStartTime: %v", err)
	}
	id := verifiedProcess{PID: os.Getpid(), StartTime: start + 1, StartTimeKnown: true}
	if err := verifyProcessIdentity(id); err == nil {
		t.Fatal("expected identity mismatch error")
	}
}

func writeStopGraph(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "g.dot")
	_ = os.WriteFile(path, []byte(`
digraph G {
  start [shape=Mdiamond]
  t [shape=parallelogram, tool_command="sleep 60"]
  exit [shape=Msquare]
  start -> t -> exit
}`), 0o644)
	return path
}
