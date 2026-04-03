// Verifies that tool-only graphs execute correctly with NewCoreRegistry (L0-only).
// This proves the engine works without Layer 1 or Layer 2 handlers registered.
package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestCoreRegistry_ToolOnlyGraph(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph core_only {
  graph [goal="Test core registry with tool-only graph"]
  start [shape=Mdiamond]
  greet [shape=parallelogram, tool_command="echo hello_world"]
  done [shape=Msquare]
  start -> greet -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "core-registry-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
		Registry:    NewCoreRegistry(),
	})
	if err != nil {
		t.Fatalf("RunWithConfig with CoreRegistry: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	stdout := filepath.Join(logsRoot, "greet", "stdout.log")
	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatalf("read greet stdout: %v", err)
	}
	if !strings.Contains(string(data), "hello_world") {
		t.Fatalf("greet stdout: got %q, want hello_world", string(data))
	}
}
