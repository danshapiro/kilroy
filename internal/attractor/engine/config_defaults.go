// Construct a RunConfigFile with sensible defaults for zero-config runs.

package engine

import (
	"fmt"
	"os"

	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
)

// DefaultRunConfig builds a RunConfigFile with sensible defaults suitable for
// running without an explicit config file. The repo path defaults to the
// current working directory if it is a git repo, otherwise an error is returned.
// Call applyConfigDefaults and validateConfig on the result before use.
func DefaultRunConfig() (*RunConfigFile, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("cannot determine working directory: %w", err)
	}
	if !gitutil.IsRepo(cwd) {
		return nil, fmt.Errorf("current directory is not a git repo; either run from a git repo or provide --config")
	}

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = cwd
	cfg.LLM.CLIProfile = "real"
	// ModelDB left empty — bootstrap will use embedded catalog.
	// CXDB left empty — already optional.

	applyConfigDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}
