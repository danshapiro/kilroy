//go:build !windows

package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestEnsureCXDBReady_GivesAutostartGuidance(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.CXDB.BinaryAddr = "127.0.0.1:65530"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:65531"
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"
	applyConfigDefaults(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, _, err := ensureCXDBReady(ctx, cfg, t.TempDir(), "test-run")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cxdb.autostart.enabled=true") {
		t.Fatalf("expected autostart guidance in error, got: %v", err)
	}
}

func TestEnsureCXDBReady_StartsUIAndReturnsURL(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	logsRoot := t.TempDir()
	marker := filepath.Join(logsRoot, "ui-marker.txt")

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.CXDB.Autostart.UI.Enabled = true
	cfg.CXDB.Autostart.UI.Command = []string{"sh", "-c", "printf ready > \"$KILROY_LOGS_ROOT/ui-marker.txt\""}
	cfg.CXDB.Autostart.UI.URL = "http://127.0.0.1:9020"
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"
	applyConfigDefaults(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, bin, info, err := ensureCXDBReady(ctx, cfg, logsRoot, "ui-run")
	if err != nil {
		t.Fatalf("ensureCXDBReady: %v", err)
	}
	defer func() { _ = bin.Close() }()

	if info == nil {
		t.Fatalf("expected startup info")
	}
	if got, want := info.UIURL, "http://127.0.0.1:9020"; got != want {
		t.Fatalf("UIURL=%q want %q", got, want)
	}
	if !info.UIStarted {
		t.Fatalf("expected UIStarted=true")
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected UI marker %s to exist", marker)
}

func TestEnsureCXDBReady_AutoDiscoversUIURLFromBase(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	logsRoot := t.TempDir()

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"
	applyConfigDefaults(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, bin, info, err := ensureCXDBReady(ctx, cfg, logsRoot, "discover-ui")
	if err != nil {
		t.Fatalf("ensureCXDBReady: %v", err)
	}
	defer func() { _ = bin.Close() }()

	if got, want := info.UIURL, cxdbSrv.URL(); got != want {
		t.Fatalf("UIURL=%q want %q", got, want)
	}
	if info.UIStarted {
		t.Fatalf("expected UIStarted=false when no UI command is configured")
	}
}

func TestResolveUIURL_PrefersConfiguredAndFallsBackToBaseProbe(t *testing.T) {
	htmlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>CXDB</body></html>"))
	}))
	defer htmlSrv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if got := resolveUIURL(ctx, "http://configured.example/ui", htmlSrv.URL); got != "http://configured.example/ui" {
		t.Fatalf("configured URL not preferred, got %q", got)
	}
	if got := resolveUIURL(ctx, "", htmlSrv.URL); got != htmlSrv.URL {
		t.Fatalf("base URL probe failed, got %q want %q", got, htmlSrv.URL)
	}
}

func TestStartBackgroundCommand_KeepsLogOpenUntilWaitCompletes(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "bg.log")
	cmdPath := filepath.Join(t.TempDir(), "writer.sh")
	if err := os.WriteFile(cmdPath, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo first-line
sleep 0.25
echo second-line
`), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	proc, err := startBackgroundCommand([]string{cmdPath}, logPath, nil)
	if err != nil {
		t.Fatalf("startBackgroundCommand: %v", err)
	}
	select {
	case waitErr := <-proc.waitCh:
		if waitErr != nil {
			t.Fatalf("background process failed: %v", waitErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for background process completion")
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read %s: %v", logPath, err)
	}
	logText := string(b)
	if !strings.Contains(logText, "first-line") || !strings.Contains(logText, "second-line") {
		t.Fatalf("expected complete delayed log output, got %q", logText)
	}
}

func TestEnsureCXDBReady_AutostartProcessTerminatedOnContextCancel(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("process group signaling is unreliable on macOS")
	}
	logsRoot := t.TempDir()
	pidPath := filepath.Join(logsRoot, "cxdb-autostart.pid")
	cmdPath := filepath.Join(logsRoot, "cxdb-autostart.sh")
	if err := os.WriteFile(cmdPath, []byte(fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
echo "$$" > %q
trap 'exit 0' TERM INT
while true; do sleep 1; done
`, pidPath)), 0o755); err != nil {
		t.Fatalf("write autostart script: %v", err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.CXDB.BinaryAddr = "127.0.0.1:65530"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:65531"
	cfg.CXDB.Autostart.Enabled = true
	cfg.CXDB.Autostart.Command = []string{cmdPath}
	cfg.CXDB.Autostart.WaitTimeoutMS = 10_000
	cfg.CXDB.Autostart.PollIntervalMS = 50
	applyConfigDefaults(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, _, _, err := ensureCXDBReady(ctx, cfg, logsRoot, "cancel-run")
	if err == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("unexpected error: %v", err)
	}

	pid := mustReadPIDFileWithin(t, pidPath, 2*time.Second)
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	waitForPIDToExit(t, pid, 5*time.Second)
}

func TestEnsureCXDBReady_UIAutostartProcessTerminatedOnRunShutdown(t *testing.T) {
	cxdbSrv := newCXDBTestServer(t)
	logsRoot := t.TempDir()
	pidPath := filepath.Join(logsRoot, "ui-autostart.pid")
	cmdPath := filepath.Join(logsRoot, "ui-autostart.sh")
	if err := os.WriteFile(cmdPath, []byte(fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
echo "$$" > %q
trap 'exit 0' TERM INT
while true; do sleep 1; done
`, pidPath)), 0o755); err != nil {
		t.Fatalf("write ui script: %v", err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.CXDB.Autostart.UI.Enabled = true
	cfg.CXDB.Autostart.UI.Command = []string{cmdPath}
	applyConfigDefaults(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, bin, startup, err := ensureCXDBReady(ctx, cfg, logsRoot, "ui-shutdown")
	if err != nil {
		t.Fatalf("ensureCXDBReady: %v", err)
	}
	defer func() { _ = bin.Close() }()
	if startup == nil || !startup.UIStarted {
		t.Fatalf("expected startup info with UIStarted=true, got %#v", startup)
	}

	pid := mustReadPIDFileWithin(t, pidPath, 2*time.Second)
	t.Cleanup(func() {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	})
	if err := startup.shutdownManagedProcesses(); err != nil {
		t.Fatalf("shutdownManagedProcesses: %v", err)
	}
	waitForPIDToExit(t, pid, 5*time.Second)
}
