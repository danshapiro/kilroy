// Tests for the output contract: declared outputs, collection, warnings.
package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestDeclaredOutputs_ParsesGraphAttribute(t *testing.T) {
	g := model.NewGraph("test")
	g.Attrs["outputs"] = "report.md, summary.json"
	got := DeclaredOutputs(g)
	if len(got) != 2 || got[0] != "report.md" || got[1] != "summary.json" {
		t.Fatalf("DeclaredOutputs = %v, want [report.md summary.json]", got)
	}
}

func TestDeclaredOutputs_EmptyWhenNotSet(t *testing.T) {
	g := model.NewGraph("test")
	if got := DeclaredOutputs(g); len(got) != 0 {
		t.Fatalf("DeclaredOutputs = %v, want empty", got)
	}
}

func TestCollectOutputs_CopiesFoundFiles(t *testing.T) {
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	_ = os.WriteFile(filepath.Join(worktree, "report.md"), []byte("# Report"), 0o644)

	results, warnings := CollectOutputs([]string{"report.md", "missing.txt"}, worktree, logsRoot)
	if len(results) != 2 {
		t.Fatalf("len(results) = %d, want 2", len(results))
	}
	if !results[0].Found || results[0].Name != "report.md" {
		t.Fatalf("result[0] = %+v, want found report.md", results[0])
	}
	if results[1].Found {
		t.Fatalf("result[1] should not be found")
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want 1 warning for missing.txt", warnings)
	}

	// Verify the collected file exists in outputs dir.
	collected := filepath.Join(logsRoot, "outputs", "report.md")
	data, err := os.ReadFile(collected)
	if err != nil {
		t.Fatalf("collected file not found: %v", err)
	}
	if string(data) != "# Report" {
		t.Fatalf("collected content = %q, want # Report", string(data))
	}
}

func TestOutputContract_Integration_CollectsAfterRun(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph output_test {
  graph [goal="Test output contract", outputs="result.txt"]
  start [shape=Mdiamond]
  produce [shape=parallelogram, tool_command="echo output_data > result.txt"]
  done [shape=Msquare]
  start -> produce -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "output-test-001",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify outputs.json was written.
	outputsJSON := filepath.Join(logsRoot, "outputs.json")
	data, err := os.ReadFile(outputsJSON)
	if err != nil {
		t.Fatalf("read outputs.json: %v", err)
	}
	var results []OutputResult
	if err := json.Unmarshal(data, &results); err != nil {
		t.Fatalf("parse outputs.json: %v", err)
	}
	if len(results) != 1 || !results[0].Found {
		t.Fatalf("outputs = %+v, want 1 found result", results)
	}

	// Verify collected file exists.
	collected := filepath.Join(logsRoot, "outputs", "result.txt")
	if _, err := os.Stat(collected); err != nil {
		t.Fatalf("collected output not found: %v", err)
	}
}
