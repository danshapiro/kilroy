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

func TestResolveModelCatalog_OnRunStartFetch_WarnsWhenDifferentFromPinned(t *testing.T) {
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

	res, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	if strings.TrimSpace(res.Warning) == "" {
		t.Fatalf("expected warning when fetched differs from pinned; got empty warning")
	}
}

func TestResolveModelCatalog_OnRunStartFetch_NoWarningWhenIdenticalToPinned(t *testing.T) {
	dir := t.TempDir()
	body := `{"data":[{"id":"openai/gpt-5"}]}`
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	res, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	if strings.TrimSpace(res.Warning) != "" {
		t.Fatalf("expected no warning when fetched equals pinned; got %q", res.Warning)
	}
}

func TestResolveModelCatalog_OnRunStartFetch_FallsBackToPinnedWithWarningOnFailure(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogOnRunStart, "http://127.0.0.1:0", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	if strings.TrimSpace(res.Warning) == "" {
		t.Fatalf("expected warning on fetch failure; got empty warning")
	}
	if res.Source != pinned {
		t.Fatalf("source: got %q want %q", res.Source, pinned)
	}
}
