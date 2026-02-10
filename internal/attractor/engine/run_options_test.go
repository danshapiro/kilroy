package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRunOptions_ApplyDefaults_DefaultLogsRootUsesXDGStateHomeAndIsOutsideRepo(t *testing.T) {
	state := t.TempDir()
	t.Setenv("XDG_STATE_HOME", state)

	repo := t.TempDir()
	opts := RunOptions{
		RepoPath: repo,
		RunID:    "01HZZZZZZZZZZZZZZZZZZZZZZZZ", // stable, filesystem-safe
	}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}

	wantPrefix := filepath.Join(state, "kilroy", "attractor", "runs", opts.RunID)
	if !strings.HasPrefix(opts.LogsRoot, wantPrefix) {
		t.Fatalf("LogsRoot: got %q want prefix %q", opts.LogsRoot, wantPrefix)
	}
	if strings.HasPrefix(opts.LogsRoot, repo+string(filepath.Separator)) || opts.LogsRoot == repo {
		t.Fatalf("LogsRoot should be outside repo: logs_root=%q repo=%q", opts.LogsRoot, repo)
	}
	if opts.WorktreeDir != filepath.Join(opts.LogsRoot, "worktree") {
		t.Fatalf("WorktreeDir: got %q want %q", opts.WorktreeDir, filepath.Join(opts.LogsRoot, "worktree"))
	}
}

func TestRunOptionsApplyDefaults_SetsMaxLLMRetriesWhenUnset(t *testing.T) {
	opts := RunOptions{
		RepoPath: t.TempDir(),
	}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	if opts.MaxLLMRetries == nil {
		t.Fatalf("MaxLLMRetries should be set by default")
	}
	if got, want := *opts.MaxLLMRetries, 6; got != want {
		t.Fatalf("MaxLLMRetries=%d want %d", got, want)
	}
}

func TestRunOptionsApplyDefaults_PreservesExplicitZeroMaxLLMRetries(t *testing.T) {
	zero := 0
	opts := RunOptions{
		RepoPath:      t.TempDir(),
		MaxLLMRetries: &zero,
	}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	if opts.MaxLLMRetries == nil {
		t.Fatalf("MaxLLMRetries should not be nil")
	}
	if got, want := *opts.MaxLLMRetries, 0; got != want {
		t.Fatalf("MaxLLMRetries=%d want %d", got, want)
	}
}
