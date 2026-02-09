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

func TestResolveModelCatalog_PinnedOnly(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogPinnedOnly, "", 0)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	if res.SnapshotPath == "" || res.SHA256 == "" {
		t.Fatalf("missing snapshot metadata: %+v", res)
	}
	if res.Source != pinned {
		t.Fatalf("source: got %q want %q", res.Source, pinned)
	}
	if !strings.HasSuffix(res.SnapshotPath, "modeldb/openrouter_models.json") {
		t.Fatalf("unexpected snapshot path: %s", res.SnapshotPath)
	}
}

func TestResolveModelCatalog_OnRunStartFetch_WritesOpenRouterSnapshotName(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"anthropic/claude-4"}]}`))
	}))
	defer srv.Close()

	res, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogOnRunStart, srv.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	if res.Source != srv.URL {
		t.Fatalf("source: got %q want %q", res.Source, srv.URL)
	}
	if !strings.HasSuffix(res.SnapshotPath, "modeldb/openrouter_models.json") {
		t.Fatalf("unexpected snapshot path: %s", res.SnapshotPath)
	}
}
