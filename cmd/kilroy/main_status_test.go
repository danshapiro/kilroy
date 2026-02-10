package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAttractorStatus_PrintsRunningState_WhenPIDAlive(t *testing.T) {
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
	_ = os.WriteFile(filepath.Join(logs, "live.json"), []byte(`{"event":"stage_attempt_start","node_id":"impl"}`), 0o644)

	out, err := exec.Command(bin, "attractor", "status", "--logs-root", logs).CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "state=running") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStatus_PrintsUnknownWithoutFinalOrLivePID(t *testing.T) {
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	_ = os.WriteFile(filepath.Join(logs, "live.json"), []byte(`{"event":"stage_attempt_start","node_id":"impl"}`), 0o644)

	out, err := exec.Command(bin, "attractor", "status", "--logs-root", logs).CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "state=unknown") {
		t.Fatalf("unexpected output: %s", out)
	}
}

func TestAttractorStatus_TerminalFinalIgnoresMalformedPID(t *testing.T) {
	bin := buildKilroyBinary(t)
	logs := t.TempDir()
	_ = os.WriteFile(filepath.Join(logs, "final.json"), []byte(`{"status":"success","run_id":"r1"}`), 0o644)
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte("bad-pid"), 0o644)

	out, err := exec.Command(bin, "attractor", "status", "--logs-root", logs).CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "state=success") {
		t.Fatalf("unexpected output: %s", out)
	}
}
