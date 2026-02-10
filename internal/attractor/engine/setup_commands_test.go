package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSetupCommands_NoConfig(t *testing.T) {
	dir := t.TempDir()
	e := &Engine{
		LogsRoot:    dir,
		WorktreeDir: dir,
		Options:     RunOptions{RunID: "test-no-config"},
	}

	// No RunConfig at all — should be a no-op.
	if err := e.executeSetupCommands(context.Background()); err != nil {
		t.Fatalf("expected no error with nil RunConfig, got: %v", err)
	}

	// RunConfig with empty commands — should be a no-op.
	e.RunConfig = &RunConfigFile{}
	if err := e.executeSetupCommands(context.Background()); err != nil {
		t.Fatalf("expected no error with empty commands, got: %v", err)
	}
}

func TestSetupCommands_RunsInWorktree(t *testing.T) {
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	e := &Engine{
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Options:     RunOptions{RunID: "test-worktree"},
		RunConfig: &RunConfigFile{
			Setup: struct {
				Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
				TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
			}{
				Commands:  []string{"pwd > cwd.txt"},
				TimeoutMS: 10000,
			},
		},
	}

	if err := e.executeSetupCommands(context.Background()); err != nil {
		t.Fatalf("executeSetupCommands failed: %v", err)
	}

	// Verify the command ran inside the worktree directory.
	b, err := os.ReadFile(filepath.Join(worktree, "cwd.txt"))
	if err != nil {
		t.Fatalf("expected cwd.txt in worktree: %v", err)
	}
	got := strings.TrimSpace(string(b))
	if got != worktree {
		t.Fatalf("command ran in %q, expected %q", got, worktree)
	}
}

func TestSetupCommands_FailsOnError(t *testing.T) {
	logsRoot := t.TempDir()
	worktree := t.TempDir()

	e := &Engine{
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Options:     RunOptions{RunID: "test-fail"},
		RunConfig: &RunConfigFile{
			Setup: struct {
				Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
				TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
			}{
				Commands:  []string{"false"},
				TimeoutMS: 10000,
			},
		},
	}

	err := e.executeSetupCommands(context.Background())
	if err == nil {
		t.Fatal("expected error from failing command")
	}
	if !strings.Contains(err.Error(), "setup command [0]") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestSetupCommands_MultipleCommands(t *testing.T) {
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	e := &Engine{
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Options:     RunOptions{RunID: "test-multi"},
		RunConfig: &RunConfigFile{
			Setup: struct {
				Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
				TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
			}{
				Commands: []string{
					"echo first > first.txt",
					"echo second > second.txt",
					"echo third > third.txt",
				},
				TimeoutMS: 10000,
			},
		},
	}

	if err := e.executeSetupCommands(context.Background()); err != nil {
		t.Fatalf("executeSetupCommands failed: %v", err)
	}

	// All three files should exist.
	for _, name := range []string{"first.txt", "second.txt", "third.txt"} {
		b, err := os.ReadFile(filepath.Join(worktree, name))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
		if strings.TrimSpace(string(b)) == "" {
			t.Fatalf("%s is empty", name)
		}
	}
}

func TestSetupCommands_FailsOnError_StopsEarly(t *testing.T) {
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	e := &Engine{
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Options:     RunOptions{RunID: "test-stop-early"},
		RunConfig: &RunConfigFile{
			Setup: struct {
				Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
				TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
			}{
				Commands: []string{
					"echo before > before.txt",
					"false",
					"echo after > after.txt",
				},
				TimeoutMS: 10000,
			},
		},
	}

	err := e.executeSetupCommands(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}

	// First command should have run.
	if _, err := os.Stat(filepath.Join(worktree, "before.txt")); err != nil {
		t.Fatalf("before.txt should exist: %v", err)
	}
	// Third command should NOT have run.
	if _, err := os.Stat(filepath.Join(worktree, "after.txt")); err == nil {
		t.Fatal("after.txt should not exist (fail-fast)")
	}
}

func TestSetupCommands_Timeout(t *testing.T) {
	worktree := t.TempDir()
	logsRoot := t.TempDir()

	e := &Engine{
		LogsRoot:    logsRoot,
		WorktreeDir: worktree,
		Options:     RunOptions{RunID: "test-timeout"},
		RunConfig: &RunConfigFile{
			Setup: struct {
				Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
				TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
			}{
				Commands:  []string{"sleep 30"},
				TimeoutMS: 200, // 200ms timeout
			},
		},
	}

	start := time.Now()
	err := e.executeSetupCommands(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error")
	}
	// Should complete well under 5s (the sleep is 30s, timeout is 200ms).
	if elapsed > 5*time.Second {
		t.Fatalf("timeout took too long: %v", elapsed)
	}
}
