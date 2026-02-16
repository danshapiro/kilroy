package gitutil

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type CommandError struct {
	Args   []string
	Stdout string
	Stderr string
	Err    error
}

func (e *CommandError) Error() string {
	msg := fmt.Sprintf("git %s: %v", strings.Join(e.Args, " "), e.Err)
	if e.Stderr != "" {
		msg += ": " + strings.TrimSpace(e.Stderr)
	}
	return msg
}

func runGit(dir string, args ...string) (string, string, error) {
	// Disable Git's background auto-maintenance (introduced as a default in newer Git versions)
	// to keep Attractor runs deterministic and to avoid spawning extra long-running helper
	// processes during frequent checkpoint commits.
	base := []string{
		"-C", dir,
		"-c", "maintenance.auto=0",
		"-c", "gc.auto=0",
	}
	cmd := exec.Command("git", append(base, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	outStr := stdout.String()
	errStr := stderr.String()
	if err != nil {
		return outStr, errStr, &CommandError{Args: args, Stdout: outStr, Stderr: errStr, Err: err}
	}
	return outStr, errStr, nil
}

func IsRepo(dir string) bool {
	out, _, err := runGit(dir, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return false
	}
	return strings.TrimSpace(out) == "true"
}

func HeadSHA(dir string) (string, error) {
	out, _, err := runGit(dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func StatusPorcelain(dir string) (string, error) {
	out, _, err := runGit(dir, "status", "--porcelain")
	if err != nil {
		return "", err
	}
	return out, nil
}

func IsClean(dir string) (bool, error) {
	out, err := StatusPorcelain(dir)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "", nil
}

func CreateBranchAt(dir, branch, baseSHA string) error {
	// Create or reset branch to baseSHA.
	_, _, err := runGit(dir, "branch", "--force", branch, baseSHA)
	return err
}

func AddWorktree(repoDir, worktreeDir, branch string) error {
	_, _, err := runGit(repoDir, "worktree", "add", worktreeDir, branch)
	return err
}

func RemoveWorktree(repoDir, worktreeDir string) error {
	_, _, err := runGit(repoDir, "worktree", "remove", "--force", worktreeDir)
	return err
}

func CheckoutBranch(worktreeDir, branch string) error {
	_, _, err := runGit(worktreeDir, "switch", branch)
	return err
}

func ResetHard(worktreeDir, sha string) error {
	_, _, err := runGit(worktreeDir, "reset", "--hard", sha)
	return err
}

func AddAll(worktreeDir string) error {
	_, _, err := runGit(worktreeDir, "add", "-A")
	return err
}

// AddAllWithExcludes stages all changes except paths matching provided git
// pathspec globs via :(exclude)<glob>.
func AddAllWithExcludes(worktreeDir string, excludes []string) error {
	args := []string{"add", "-A", "--", "."}
	for _, p := range excludes {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.HasPrefix(p, ":(") {
			args = append(args, p)
			continue
		}
		args = append(args, ":(glob,exclude)"+p)
	}
	_, _, err := runGit(worktreeDir, args...)
	return err
}

func CommitAllowEmpty(worktreeDir, message string) (string, error) {
	return CommitAllowEmptyWithExcludes(worktreeDir, message, nil)
}

func CommitAllowEmptyWithExcludes(worktreeDir, message string, excludes []string) (string, error) {
	if err := AddAllWithExcludes(worktreeDir, excludes); err != nil {
		return "", err
	}
	return commitAllowEmpty(worktreeDir, message)
}

func commitAllowEmpty(worktreeDir, message string) (string, error) {
	_, _, err := runGit(worktreeDir, "commit", "--allow-empty", "-m", message)
	if err != nil {
		// If identity is missing, retry once with an explicit fallback committer identity
		// (without mutating repo config).
		if strings.Contains(err.Error(), "Author identity unknown") ||
			strings.Contains(err.Error(), "Please tell me who you are") ||
			strings.Contains(err.Error(), "unable to auto-detect email address") {
			_, _, err = runGit(
				worktreeDir,
				"-c", "user.name=kilroy-attractor",
				"-c", "user.email=kilroy-attractor@local",
				"commit", "--allow-empty", "-m", message,
			)
		}
		if err != nil {
			return "", err
		}
	}
	return HeadSHA(worktreeDir)
}

// PushBranch pushes a branch to the specified remote.
// It is a best-effort operation; failures are returned but should not abort a run.
func PushBranch(repoDir, remote, branch string) error {
	_, _, err := runGit(repoDir, "push", remote, branch)
	return err
}

func MergeFastForwardOnly(worktreeDir, otherRef string) error {
	_, _, err := runGit(worktreeDir, "merge", "--ff-only", otherRef)
	return err
}

// FastForwardFFOnly fast-forwards the currently checked out branch to otherRef (commit SHA or ref),
// failing if a non-fast-forward merge would be required.
func FastForwardFFOnly(worktreeDir, otherRef string) error {
	return MergeFastForwardOnly(worktreeDir, otherRef)
}

// DiffNameOnly returns file paths changed between baseRef and HEAD in the given directory.
func DiffNameOnly(dir, baseRef string) ([]string, error) {
	out, _, err := runGit(dir, "diff", "--name-only", baseRef)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			files = append(files, trimmed)
		}
	}
	return files, nil
}

func ensureUserIdentity(worktreeDir string) error {
	name, _, err := runGit(worktreeDir, "config", "--get", "user.name")
	if err != nil {
		// config --get exits 1 when missing; treat as empty.
		name = ""
	}
	email, _, err := runGit(worktreeDir, "config", "--get", "user.email")
	if err != nil {
		email = ""
	}
	name = strings.TrimSpace(name)
	email = strings.TrimSpace(email)
	if name == "" {
		_, _, _ = runGit(worktreeDir, "config", "user.name", "kilroy-attractor")
	}
	if email == "" {
		_, _, _ = runGit(worktreeDir, "config", "user.email", "kilroy-attractor@local")
	}
	return nil
}
