package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	// Register GitOps auto-detection for tests so that tests creating
	// git repos get git worktree behavior without explicitly passing GitOps.
	AutoDetectGitOps = func(repoPath string) GitOps {
		hook := &testGitOps{}
		if hook.ValidateRepo(repoPath, false) == nil {
			return hook
		}
		return nil
	}

	stateRoot, err := os.MkdirTemp("", "kilroy-engine-test-state-*")
	if err != nil {
		_, _ = os.Stderr.WriteString("failed to create temp state root: " + err.Error() + "\n")
		os.Exit(2)
	}

	if strings.TrimSpace(os.Getenv("XDG_STATE_HOME")) == "" {
		_ = os.Setenv("XDG_STATE_HOME", stateRoot)
	}
	if strings.TrimSpace(os.Getenv("KILROY_CODEX_STATE_BASE")) == "" {
		_ = os.Setenv("KILROY_CODEX_STATE_BASE", filepath.Join(stateRoot, "kilroy", "attractor", "codex-state"))
	}

	code := m.Run()
	_ = os.RemoveAll(stateRoot)
	os.Exit(code)
}
