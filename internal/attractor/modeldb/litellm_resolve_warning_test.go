package modeldb

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

func TestResolveLiteLLMCatalog_DelegatesToResolveModelCatalog_OnRunStartWarning(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"anthropic/claude-4"}]}`))
	}))
	t.Cleanup(srv.Close)

	expected, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	got, err := ResolveLiteLLMCatalog(context.Background(), pinned, dir, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ResolveLiteLLMCatalog: %v", err)
	}
	if strings.TrimSpace(got.Warning) == "" {
		t.Fatalf("expected warning when fetched differs from pinned; got empty warning")
	}
	if got.Warning != expected.Warning {
		t.Fatalf("warning: got %q want %q", got.Warning, expected.Warning)
	}
	if got.Source != expected.Source {
		t.Fatalf("source: got %q want %q", got.Source, expected.Source)
	}
	if got.SHA256 != expected.SHA256 {
		t.Fatalf("sha256: got %q want %q", got.SHA256, expected.SHA256)
	}
	if got.SnapshotPath != expected.SnapshotPath {
		t.Fatalf("snapshot path: got %q want %q", got.SnapshotPath, expected.SnapshotPath)
	}
}
