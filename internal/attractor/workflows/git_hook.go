// GitOps implementation using gitutil for version-controlled run workspaces.
// Registered as an optional hook — runs work without it in plain directories.
package workflows

import (
	"fmt"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
)

// GitHook implements engine.GitOps using real git operations. It provides
// worktree isolation, per-node commits, and branch management for runs
// executing inside a git repository.
type GitHook struct{}

var _ engine.GitOps = (*GitHook)(nil)

func (g *GitHook) ValidateRepo(repoPath string, requireClean bool) error {
	if !gitutil.IsRepo(repoPath) {
		return fmt.Errorf("not a git repo: %s", repoPath)
	}
	if requireClean {
		clean, err := gitutil.IsClean(repoPath)
		if err != nil {
			return err
		}
		if !clean {
			return fmt.Errorf("repo has uncommitted changes (require_clean=true)")
		}
	}
	return nil
}

func (g *GitHook) HeadSHA(dir string) (string, error) {
	return gitutil.HeadSHA(dir)
}

func (g *GitHook) SetupRunWorkspace(repoPath, worktreeDir, runBranch, baseSHA string) error {
	if err := gitutil.CreateBranchAt(repoPath, runBranch, baseSHA); err != nil {
		return err
	}
	// Remove existing worktree if present (e.g. re-run).
	_ = gitutil.RemoveWorktree(repoPath, worktreeDir)
	return gitutil.AddWorktree(repoPath, worktreeDir, runBranch)
}

func (g *GitHook) Checkpoint(worktreeDir, msg string, excludes []string) (string, error) {
	return gitutil.CommitAllowEmptyWithExcludes(worktreeDir, msg, excludes)
}

func (g *GitHook) CheckpointSimple(worktreeDir, msg string) (string, error) {
	return gitutil.CommitAllowEmpty(worktreeDir, msg)
}

func (g *GitHook) VerifyHeadSHA(worktreeDir, expectedSHA string) error {
	head, err := gitutil.HeadSHA(worktreeDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(head) != strings.TrimSpace(expectedSHA) {
		return fmt.Errorf("handler-provided checkpoint sha does not match HEAD (head=%s meta=%s)", head, expectedSHA)
	}
	return nil
}

func (g *GitHook) CopyIgnoredFiles(src, dst string, excludePrefixes ...string) error {
	return gitutil.CopyIgnoredFiles(src, dst, excludePrefixes...)
}

func (g *GitHook) SetupBranchWorkspace(repoPath, worktreeDir, branchName, baseSHA string) error {
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

func (g *GitHook) RepairWorktree(repoPath, worktreeDir string) error {
	return gitutil.RepairWorktree(repoPath, worktreeDir)
}

func (g *GitHook) MergeBranch(parentDir, headSHA string) error {
	return gitutil.FastForwardFFOnly(parentDir, headSHA)
}

func (g *GitHook) ResumeWorkspace(repoPath, worktreeDir, runBranch, checkpointSHA string) error {
	_ = gitutil.RemoveWorktree(repoPath, worktreeDir)
	if err := gitutil.CreateBranchAt(repoPath, runBranch, checkpointSHA); err != nil {
		return err
	}
	if err := gitutil.AddWorktree(repoPath, worktreeDir, runBranch); err != nil {
		return err
	}
	return gitutil.ResetHard(worktreeDir, checkpointSHA)
}

func (g *GitHook) PushBranch(repoPath, remote, branch string) error {
	return gitutil.PushBranch(repoPath, remote, branch)
}

func (g *GitHook) RemoveWorktree(repoPath, worktreeDir string) error {
	return gitutil.RemoveWorktree(repoPath, worktreeDir)
}
