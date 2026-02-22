package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_FailsWhenRepoIsDirty(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Make the repo dirty.
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello2\n"), 0o644)

	dot := []byte(`digraph G { start [shape=Mdiamond] exit [shape=Msquare] start -> exit }`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "uncommitted changes") {
		t.Fatalf("error: %v", err)
	}
}

func TestRun_FailsWhenNotAGitRepo(t *testing.T) {
	dir := t.TempDir()
	dot := []byte(`digraph G { start [shape=Mdiamond] exit [shape=Msquare] start -> exit }`)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: dir})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
