package engine

import (
	"context"
	"path/filepath"
	"strings"
)

// PreflightResult contains metadata emitted by preflight-only startup checks.
type PreflightResult struct {
	RunID               string
	LogsRoot            string
	PreflightReportPath string
	Warnings            []string
	CXDBUIURL           string
}

// PreflightWithConfig runs all RunWithConfig prechecks and exits before pipeline startup.
func PreflightWithConfig(ctx context.Context, dotSource []byte, cfg *RunConfigFile, overrides RunOptions) (*PreflightResult, error) {
	boot, err := bootstrapRunWithConfig(ctx, dotSource, cfg, overrides)
	if err != nil {
		return nil, err
	}
	defer closeRunBootstrapResources(boot)

	cxdbUI := ""
	if boot.Startup != nil {
		cxdbUI = strings.TrimSpace(boot.Startup.UIURL)
	}

	return &PreflightResult{
		RunID:               boot.Options.RunID,
		LogsRoot:            boot.Options.LogsRoot,
		PreflightReportPath: filepath.Join(boot.Options.LogsRoot, "preflight_report.json"),
		Warnings:            append([]string{}, boot.Warnings...),
		CXDBUIURL:           cxdbUI,
	}, nil
}
