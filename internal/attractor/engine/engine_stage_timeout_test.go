package engine

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// Intentionally uses shape=parallelogram/tool_command because this is the
// existing supported ToolHandler path in the current engine.
func TestRun_GlobalStageTimeoutCapsToolNode(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	// The wait node times out (sleep 2 with 100ms stage timeout), producing
	// a fail outcome. An unconditional edge routes to exit regardless.
	dot := []byte(`digraph G {
  graph [default_max_retry=0]
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 100 * time.Millisecond}
	start := time.Now()
	_, err := Run(context.Background(), dot, opts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// The timeout should fire at 100ms, not let the sleep run for 2s.
	if elapsed > 3*time.Second {
		t.Fatalf("expected stage timeout to cap tool execution; took %v", elapsed)
	}
}

func TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	// Node timeout is 1s, global is 5s, so 1s should apply. sleep 2
	// exceeds 1s, so the tool times out quickly rather than waiting 2s.
	dot := []byte(`digraph G {
  graph [default_max_retry=0]
  start [shape=Mdiamond]
  wait [shape=parallelogram, timeout="1s", tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 5 * time.Second}
	// The smaller timeout (1s) should apply, not 5s.
	start := time.Now()
	_, _ = Run(context.Background(), dot, opts)
	elapsed := time.Since(start)
	if elapsed > 3*time.Second {
		t.Fatalf("expected smaller timeout (1s) to apply; took %v (likely used 5s global)", elapsed)
	}
}

// TestRun_TimeoutOutcomeIncludesMetadata verifies that timed-out nodes get
// enriched Meta with timeout=true and a partial_status.json diagnostic artifact.
// Uses StageTimeout (engine-level) rather than node timeout to ensure the engine
// context deadline fires — the ToolHandler applies node timeout internally.
func TestRun_TimeoutOutcomeIncludesMetadata(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 5"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit
}`)
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	// Use global StageTimeout to set the engine-level context deadline.
	opts := RunOptions{RepoPath: repo, LogsRoot: logsRoot, StageTimeout: 500 * time.Millisecond}
	_, _ = Run(context.Background(), dot, opts)

	statusPath := filepath.Join(logsRoot, "wait", "status.json")
	b, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode status.json: %v", err)
	}
	if out.Meta == nil {
		t.Fatal("expected Meta to be populated on timeout outcome")
	}
	if v, ok := out.Meta["timeout"]; !ok || v != true {
		t.Fatalf("Meta[timeout]: got %v want true", out.Meta["timeout"])
	}

	// Also verify partial_status.json was written.
	partialPath := filepath.Join(logsRoot, "wait", "partial_status.json")
	pb, err := os.ReadFile(partialPath)
	if err != nil {
		t.Fatalf("read partial_status.json: %v", err)
	}
	var partial map[string]any
	if err := json.Unmarshal(pb, &partial); err != nil {
		t.Fatalf("unmarshal partial_status.json: %v", err)
	}
	if partial["harvested"] != true {
		t.Fatalf("partial_status.json: expected harvested=true, got %v", partial["harvested"])
	}
}
