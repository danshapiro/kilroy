package runtime

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheckpoint_SaveLoad_RoundTripsAndFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "checkpoint.json")

	cp := &Checkpoint{
		Timestamp:      time.Unix(123, 0).UTC(),
		CurrentNode:    "n1",
		CompletedNodes: []string{"start", "n1"},
		NodeRetries:    map[string]int{"n1": 2},
		ContextValues:  map[string]any{"k": "v"},
		Logs:           []string{"warn"},
		GitCommitSHA:   "abc",
	}
	if err := cp.Save(p); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}

	loaded, err := LoadCheckpoint(p)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if loaded.CurrentNode != "n1" || loaded.GitCommitSHA != "abc" {
		t.Fatalf("loaded: %+v", loaded)
	}
	if loaded.NodeRetries == nil || loaded.ContextValues == nil || loaded.CompletedNodes == nil || loaded.Logs == nil || loaded.Extra == nil {
		t.Fatalf("expected non-nil collections: %+v", loaded)
	}
}
