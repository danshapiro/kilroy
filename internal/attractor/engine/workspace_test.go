// Tests for workspace abstraction: graph location vs execution location.
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

func TestWorkspace_ToolCommandRunsInWorkspace(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Create a committed file in the repo that the tool command will read.
	_ = os.WriteFile(filepath.Join(repo, "workspace-marker.txt"), []byte("found_it"), 0o644)
	runCmd(t, repo, "git", "add", "workspace-marker.txt")
	runCmd(t, repo, "git", "commit", "-m", "add marker")

	// Graph lives outside the workspace (different directory).
	graphDir := t.TempDir()
	dot := []byte(`digraph workspace_test {
  graph [goal="Test workspace abstraction"]
  start [shape=Mdiamond]
  check [shape=parallelogram, tool_command="cat workspace-marker.txt"]
  done [shape=Msquare]
  start -> check -> done
}`)
	_ = os.WriteFile(filepath.Join(graphDir, "graph.dot"), dot, 0o644)

	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "workspace-test-001",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
		Workspace:   repo,
		GraphDir:    graphDir,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	stdout, err := os.ReadFile(filepath.Join(logsRoot, "check", "stdout.log"))
	if err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	if !strings.Contains(string(stdout), "found_it") {
		t.Fatalf("stdout = %q, expected workspace-marker.txt content", string(stdout))
	}
}

func TestWorkspace_DefaultsToCwd(t *testing.T) {
	opts := RunOptions{}
	_ = opts.applyDefaults()
	// When workspace is empty, RepoPath should not be set by workspace logic.
	if opts.Workspace != "" {
		t.Fatalf("workspace should be empty when not set")
	}
}

func TestWorkspace_SetsRepoPath(t *testing.T) {
	opts := RunOptions{Workspace: "/tmp/myworkspace"}
	_ = opts.applyDefaults()
	if opts.RepoPath != "/tmp/myworkspace" {
		t.Fatalf("RepoPath = %q, want /tmp/myworkspace", opts.RepoPath)
	}
}

func TestWorkspace_RepoPathNotOverridden(t *testing.T) {
	opts := RunOptions{Workspace: "/tmp/ws", RepoPath: "/tmp/repo"}
	_ = opts.applyDefaults()
	// RepoPath should keep its explicit value.
	if opts.RepoPath != "/tmp/repo" {
		t.Fatalf("RepoPath = %q, want /tmp/repo", opts.RepoPath)
	}
}
