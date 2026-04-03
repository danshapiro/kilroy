// Tests for worktree file-not-found hint in ToolHandler error paths.

package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractLeadingPath(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"./scripts/check.sh", "./scripts/check.sh"},
		{"scripts/check.sh --flag", "scripts/check.sh"},
		{"bash -c 'scripts/check.sh'", "scripts/check.sh"},
		{"sh -c \"./run.sh arg1 arg2\"", "./run.sh"},
		{"echo hello", ""},  // bare command, no path
		{"ls", ""},          // bare command
		{"node app.js", ""}, // first token is bare command
		{"", ""},            // empty
		{"  ./test.sh  ", "./test.sh"},
	}
	for _, tt := range tests {
		got := extractLeadingPath(tt.cmd)
		if got != tt.want {
			t.Errorf("extractLeadingPath(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestWorktreeNotFoundHint_FileInRepoNotWorktree(t *testing.T) {
	repoDir := t.TempDir()
	worktreeDir := t.TempDir()

	// Create the file in repo but not in worktree.
	scriptDir := filepath.Join(repoDir, "scripts")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "check.sh"), []byte("#!/bin/bash\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine: &Engine{
			Options: RunOptions{RepoPath: repoDir},
		},
	}
	stderr := []byte("bash: scripts/check.sh: No such file or directory")
	hint := worktreeNotFoundHint(stderr, "scripts/check.sh", execCtx)
	if hint == "" {
		t.Fatal("expected a hint, got empty string")
	}
	if want := "exists in the source repo but not in the worktree"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
	if want := "git add"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
}

func TestWorktreeNotFoundHint_FileInNeither(t *testing.T) {
	repoDir := t.TempDir()
	worktreeDir := t.TempDir()

	execCtx := &Execution{
		WorktreeDir: worktreeDir,
		Engine: &Engine{
			Options: RunOptions{RepoPath: repoDir},
		},
	}
	stderr := []byte("bash: scripts/missing.sh: No such file or directory")
	hint := worktreeNotFoundHint(stderr, "scripts/missing.sh", execCtx)
	if hint == "" {
		t.Fatal("expected a hint, got empty string")
	}
	if want := "not found in worktree or source repo"; !strings.Contains(hint, want) {
		t.Errorf("hint %q should contain %q", hint, want)
	}
}

func TestWorktreeNotFoundHint_NoNotFoundInStderr(t *testing.T) {
	execCtx := &Execution{
		WorktreeDir: t.TempDir(),
		Engine: &Engine{
			Options: RunOptions{RepoPath: t.TempDir()},
		},
	}
	stderr := []byte("some other error")
	hint := worktreeNotFoundHint(stderr, "scripts/check.sh", execCtx)
	if hint != "" {
		t.Errorf("expected empty hint for unrelated error, got %q", hint)
	}
}
