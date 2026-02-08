package engine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureCXDBReady_GivesAutostartGuidance(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.CXDB.BinaryAddr = "127.0.0.1:65530"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:65531"
	cfg.ModelDB.LiteLLMCatalogPath = "/tmp/catalog.json"
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
	cfg.ModelDB.LiteLLMCatalogPath = "/tmp/catalog.json"
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
	cfg.ModelDB.LiteLLMCatalogPath = "/tmp/catalog.json"
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
