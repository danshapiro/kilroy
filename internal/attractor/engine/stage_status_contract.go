package engine

import (
	"path/filepath"
	"strings"
)

const (
	stageStatusPathEnvKey         = "KILROY_STAGE_STATUS_PATH"
	stageStatusFallbackPathEnvKey = "KILROY_STAGE_STATUS_FALLBACK_PATH"
)

type stageStatusContract struct {
	PrimaryPath    string
	FallbackPath   string
	PromptPreamble string
	EnvVars        map[string]string
	Fallbacks      []fallbackStatusPath
}

func buildStageStatusContract(worktreeDir string) stageStatusContract {
	wt := strings.TrimSpace(worktreeDir)
	if wt == "" {
		return stageStatusContract{}
	}
	wtAbs, err := filepath.Abs(wt)
	if err != nil {
		return stageStatusContract{}
	}
	primary := filepath.Join(wtAbs, "status.json")
	fallback := filepath.Join(wtAbs, ".ai", "status.json")
	promptPreamble := mustRenderStageStatusContractPromptPreamble(primary, fallback)

	return stageStatusContract{
		PrimaryPath:    primary,
		FallbackPath:   fallback,
		PromptPreamble: promptPreamble,
		EnvVars: map[string]string{
			stageStatusPathEnvKey:         primary,
			stageStatusFallbackPathEnvKey: fallback,
		},
		Fallbacks: []fallbackStatusPath{
			{
				path:   primary,
				source: statusSourceWorktree,
			},
			{
				path:   fallback,
				source: statusSourceDotAI,
			},
		},
	}
}
