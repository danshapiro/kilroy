// Construct a RunConfigFile with sensible defaults for zero-config runs.

package engine

import (
	"fmt"
	"os"
)

// DefaultRunConfig builds a RunConfigFile with sensible defaults suitable for
// running without an explicit config file. When gitOps is non-nil, the repo
// path defaults to the current working directory if it is a valid repository.
// When gitOps is nil, the repo path is left empty (no-git mode).
func DefaultRunConfig(gitOps GitOps) (*RunConfigFile, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot determine working directory: %w", err)
	}

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.LLM.CLIProfile = "real"

	if gitOps != nil {
		if err := gitOps.ValidateRepo(cwd, false); err != nil {
			return nil, fmt.Errorf("current directory is not a git repo; either run from a git repo or provide --config")
		}
		cfg.Repo.Path = cwd
	} else {
		cfg.Repo.Path = cwd
	}

	applyConfigDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
