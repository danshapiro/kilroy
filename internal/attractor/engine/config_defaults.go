// Construct a RunConfigFile with sensible defaults for zero-config runs.

package engine

import (
	"fmt"
	"os"
)

// DefaultRunConfig builds a RunConfigFile with sensible defaults suitable for
// running without an explicit config file. repoPath overrides the repository
// path; when empty, it defaults to cwd. When gitOps is non-nil, the repo path
// must be a valid git repository. When gitOps is nil, the path is used as-is.
func DefaultRunConfig(gitOps GitOps, repoPath string) (*RunConfigFile, error) {
	if repoPath == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("cannot determine working directory: %w", err)
		}
		repoPath = cwd
	}

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.LLM.CLIProfile = "real"
	cfg.Repo.Path = repoPath

	if gitOps != nil {
		if err := gitOps.ValidateRepo(repoPath, false); err != nil {
			return nil, fmt.Errorf("%s is not a git repo; either run from a git repo or provide --config", repoPath)
		}
	}

	applyConfigDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
