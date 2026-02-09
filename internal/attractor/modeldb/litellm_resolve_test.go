package modeldb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveLiteLLMCatalog_DelegatesToResolveModelCatalog_PinnedOnly(t *testing.T) {
	dir := t.TempDir()
	pinned := filepath.Join(dir, "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	expected, err := ResolveModelCatalog(context.Background(), pinned, dir, CatalogPinnedOnly, "", 0)
	if err != nil {
		t.Fatalf("ResolveModelCatalog: %v", err)
	}
	got, err := ResolveLiteLLMCatalog(context.Background(), pinned, dir, CatalogPinnedOnly, "", 0)
	if err != nil {
		t.Fatalf("ResolveLiteLLMCatalog: %v", err)
	}
	if got.SnapshotPath != expected.SnapshotPath {
		t.Fatalf("snapshot path: got %q want %q", got.SnapshotPath, expected.SnapshotPath)
	}
	if got.Source != expected.Source {
		t.Fatalf("source: got %q want %q", got.Source, expected.Source)
	}
	if got.SHA256 != expected.SHA256 {
		t.Fatalf("sha256: got %q want %q", got.SHA256, expected.SHA256)
	}
	if got.Warning != expected.Warning {
		t.Fatalf("warning: got %q want %q", got.Warning, expected.Warning)
	}
}
