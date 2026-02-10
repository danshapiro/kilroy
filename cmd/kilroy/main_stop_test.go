package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestAttractorStop_KillsProcessFromRunPID(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	bin := buildKilroyBinary(t)
	logs := t.TempDir()

	startOut, err := exec.Command("bash", "-lc", "sleep 60 >/dev/null 2>&1 & echo -n $!").CombinedOutput()
	if err != nil {
		t.Fatalf("start detached sleep: %v\n%s", err, startOut)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(startOut)))
	if err != nil || pid <= 0 {
		t.Fatalf("parse pid from %q: %v", strings.TrimSpace(string(startOut)), err)
	}
	_ = os.WriteFile(filepath.Join(logs, "run.pid"), []byte(strconv.Itoa(pid)), 0o644)

	out, err := exec.Command(bin, "attractor", "stop", "--logs-root", logs, "--grace-ms", "500", "--force").CombinedOutput()
	if err != nil {
		t.Fatalf("stop failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "stopped=") {
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
