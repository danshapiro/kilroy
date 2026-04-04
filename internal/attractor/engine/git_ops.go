// Version control operations used by the engine at lifecycle points.
// When nil, the engine operates without version control.
package engine

// AutoDetectGitOps is a pluggable factory for automatic GitOps detection.
// When set, eng.run() calls this with the repo path. If it returns a non-nil
// GitOps, git mode is enabled automatically. This allows the engine package
// to remain free of gitutil imports while preserving backward compatibility.
//
// Set by cmd/kilroy/ at startup (or by test helpers).
var AutoDetectGitOps func(repoPath string) GitOps

// GitOps encapsulates version control operations. The engine calls these
// at lifecycle points (run setup, checkpoint, parallel branch management).
// When Engine.GitOps is nil, the engine operates in "no-git" mode: it uses
// the workspace directory directly and skips commits/branches.
type GitOps interface {
	// ValidateRepo checks that the directory is a usable repository.
	// Called during run setup and resume. When requireClean is true,
	// returns an error if the repo has uncommitted changes.
	ValidateRepo(repoPath string, requireClean bool) error

	// HeadSHA returns the current HEAD commit identifier for a directory.
	HeadSHA(dir string) (string, error)

	// SetupRunWorkspace creates a branch and worktree for the run.
	// repoPath is the source repository, worktreeDir is the target path,
	// runBranch is the branch name to create, baseSHA is the starting commit.
	SetupRunWorkspace(repoPath, worktreeDir, runBranch, baseSHA string) error

	// Checkpoint commits all workspace changes and returns the commit SHA.
	// msg is the commit message. excludes is a list of glob patterns to
	// exclude from staging.
	Checkpoint(worktreeDir, msg string, excludes []string) (sha string, err error)

	// CheckpointSimple commits all workspace changes (no excludes).
	CheckpointSimple(worktreeDir, msg string) (sha string, err error)

	// VerifyHeadSHA checks that the current HEAD matches the expected SHA.
	VerifyHeadSHA(worktreeDir, expectedSHA string) error

	// CopyIgnoredFiles copies version-control-ignored files between directories.
	// excludePrefixes is an optional list of path prefixes to skip.
	CopyIgnoredFiles(src, dst string, excludePrefixes ...string) error

	// SetupBranchWorkspace creates an isolated worktree for a parallel branch.
	SetupBranchWorkspace(repoPath, worktreeDir, branchName, baseSHA string) error

	// RepairWorktree fixes the .git pointer in a worktree directory.
	RepairWorktree(repoPath, worktreeDir string) error

	// MergeBranch fast-forwards the parent worktree to the given commit SHA.
	MergeBranch(parentDir, headSHA string) error

	// ResumeWorkspace recreates a worktree at a checkpoint commit for resume.
	ResumeWorkspace(repoPath, worktreeDir, runBranch, checkpointSHA string) error

	// PushBranch pushes a branch to a remote.
	PushBranch(repoPath, remote, branch string) error

	// RemoveWorktree cleans up a worktree directory.
	RemoveWorktree(repoPath, worktreeDir string) error
}
