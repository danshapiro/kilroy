// Test helper providing a GitOps implementation for engine tests.
// Uses gitutil directly — only available in test files.
package engine

import (
	"fmt"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
)

// testGitOps implements GitOps for tests using real git operations.
type testGitOps struct{}

func (g *testGitOps) ValidateRepo(repoPath string, requireClean bool) error {
	if !gitutil.IsRepo(repoPath) {
		return fmt.Errorf("not a git repo: %s", repoPath)
	}
	if requireClean {
		clean, err := gitutil.IsClean(repoPath)
		if err != nil {
			return err
		}
		if !clean {
			return fmt.Errorf("repo has uncommitted changes")
		}
	}
	return nil
}

func (g *testGitOps) HeadSHA(dir string) (string, error) {
	return gitutil.HeadSHA(dir)
}

func (g *testGitOps) SetupRunWorkspace(repoPath, worktreeDir, runBranch, baseSHA string) error {
	if err := gitutil.CreateBranchAt(repoPath, runBranch, baseSHA); err != nil {
		return err
	}
	_ = gitutil.RemoveWorktree(repoPath, worktreeDir)
	return gitutil.AddWorktree(repoPath, worktreeDir, runBranch)
}

func (g *testGitOps) Checkpoint(worktreeDir, msg string, excludes []string) (string, error) {
	return gitutil.CommitAllowEmptyWithExcludes(worktreeDir, msg, excludes)
}

func (g *testGitOps) CheckpointSimple(worktreeDir, msg string) (string, error) {
	return gitutil.CommitAllowEmpty(worktreeDir, msg)
}

func (g *testGitOps) VerifyHeadSHA(worktreeDir, expectedSHA string) error {
	head, err := gitutil.HeadSHA(worktreeDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != strings.TrimSpace(expectedSHA) {
		return fmt.Errorf("HEAD mismatch: got %s, want %s", head, expectedSHA)
	}
	return nil
}

func (g *testGitOps) CopyIgnoredFiles(src, dst string, excludePrefixes ...string) error {
	return gitutil.CopyIgnoredFiles(src, dst, excludePrefixes...)
}

func (g *testGitOps) SetupBranchWorkspace(repoPath, worktreeDir, branchName, baseSHA string) error {
	_ = gitutil.RemoveWorktree(repoPath, worktreeDir)
	if err := gitutil.CreateBranchAt(repoPath, branchName, baseSHA); err != nil {
		return err
	}
	if err := gitutil.AddWorktree(repoPath, worktreeDir, branchName); err != nil {
		return err
	}
	_ = gitutil.ResetHard(worktreeDir, baseSHA)
	return nil
}

func (g *testGitOps) RepairWorktree(repoPath, worktreeDir string) error {
	return gitutil.RepairWorktree(repoPath, worktreeDir)
}

func (g *testGitOps) MergeBranch(parentDir, headSHA string) error {
	return gitutil.FastForwardFFOnly(parentDir, headSHA)
}

func (g *testGitOps) ResumeWorkspace(repoPath, worktreeDir, runBranch, checkpointSHA string) error {
	_ = gitutil.RemoveWorktree(repoPath, worktreeDir)
	if err := gitutil.CreateBranchAt(repoPath, runBranch, checkpointSHA); err != nil {
		return err
	}
	if err := gitutil.AddWorktree(repoPath, worktreeDir, runBranch); err != nil {
		return err
	}
	return gitutil.ResetHard(worktreeDir, checkpointSHA)
}

func (g *testGitOps) PushBranch(repoPath, remote, branch string) error {
	return gitutil.PushBranch(repoPath, remote, branch)
}

func (g *testGitOps) RemoveWorktree(repoPath, worktreeDir string) error {
	return gitutil.RemoveWorktree(repoPath, worktreeDir)
}

func (g *testGitOps) DiffStat(dir, fromSHA, toSHA string) (int, int, int, error) {
	return gitutil.DiffStat(dir, fromSHA, toSHA)
}
