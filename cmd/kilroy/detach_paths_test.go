package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDetachedPaths_ConvertsRelativeToAbsolute(t *testing.T) {
	tempDir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir temp: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	gotGraph, gotConfig, gotLogs, err := resolveDetachedPaths("g.dot", "run.yaml", "logs")
	if err != nil {
		t.Fatalf("resolveDetachedPaths: %v", err)
	}
	if !filepath.IsAbs(gotGraph) {
		t.Fatalf("graph path should be absolute: %q", gotGraph)
	}
	if !filepath.IsAbs(gotConfig) {
		t.Fatalf("config path should be absolute: %q", gotConfig)
	}
	if !filepath.IsAbs(gotLogs) {
		t.Fatalf("logs path should be absolute: %q", gotLogs)
	}
	if gotGraph != filepath.Join(tempDir, "g.dot") {
		t.Fatalf("graph path mismatch: got %q want %q", gotGraph, filepath.Join(tempDir, "g.dot"))
	}
	if gotConfig != filepath.Join(tempDir, "run.yaml") {
		t.Fatalf("config path mismatch: got %q want %q", gotConfig, filepath.Join(tempDir, "run.yaml"))
	}
	if gotLogs != filepath.Join(tempDir, "logs") {
		t.Fatalf("logs path mismatch: got %q want %q", gotLogs, filepath.Join(tempDir, "logs"))
	}
}
