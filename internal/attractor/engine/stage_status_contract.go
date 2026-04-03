package engine

import (
	"os"
	"path/filepath"
	"strings"
)

const (
	stageStatusPathEnvKey         = "KILROY_STAGE_STATUS_PATH"
	stageStatusFallbackPathEnvKey = "KILROY_STAGE_STATUS_FALLBACK_PATH"
)

type StageStatusContract struct {
	PrimaryPath    string
	FallbackPath   string
	PromptPreamble string
	EnvVars        map[string]string
	Fallbacks      []FallbackStatusPath
}

func BuildStageStatusContract(worktreeDir string) StageStatusContract {
	wt := strings.TrimSpace(worktreeDir)
	if wt == "" {
		return StageStatusContract{}
	}
	wtAbs, err := filepath.Abs(wt)
	if err != nil {
		return StageStatusContract{}
	}
	primary := filepath.Join(wtAbs, "status.json")
	fallback := filepath.Join(runScopedWorktreeRoot(wtAbs, inferRunIDForStatusFallback(wtAbs)), "status.json")
	promptPreamble := mustRenderStageStatusContractPromptPreamble(primary, fallback)

	return StageStatusContract{
		PrimaryPath:    primary,
		FallbackPath:   fallback,
		PromptPreamble: promptPreamble,
		EnvVars: map[string]string{
			stageStatusPathEnvKey:         primary,
			stageStatusFallbackPathEnvKey: fallback,
		},
		Fallbacks: []FallbackStatusPath{
			{
				Path:   primary,
				Source: StatusSourceWorktree,
			},
			{
				Path:   fallback,
				Source: StatusSourceDotAI,
			},
		},
	}
}

func inferRunIDForStatusFallback(worktreeDir string) string {
	if runID := strings.TrimSpace(os.Getenv(runIDEnvKey)); runID != "" {
		return runID
	}
	if runID := inferRunIDFromWorktreeGitHEAD(worktreeDir); runID != "" {
		return runID
	}
	// Keep run-scoped layout even if run ID inference fails.
	return "unknown_run"
}

func inferRunIDFromWorktreeGitHEAD(worktreeDir string) string {
	gitDir := resolveWorktreeGitDir(worktreeDir)
	if gitDir == "" {
		return ""
	}
	headBytes, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	head := strings.TrimSpace(string(headBytes))
	if !strings.HasPrefix(head, "ref:") {
		return ""
	}
	ref := strings.TrimSpace(strings.TrimPrefix(head, "ref:"))
	if ref == "" {
		return ""
	}
	ref = strings.Trim(ref, "/")
	parts := strings.Split(ref, "/")
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[len(parts)-1])
}

func resolveWorktreeGitDir(worktreeDir string) string {
	gitPath := filepath.Join(strings.TrimSpace(worktreeDir), ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return gitPath
	}
	content, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(content))
	if !strings.HasPrefix(line, "gitdir:") {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(strings.TrimSpace(worktreeDir), gitDir)
	}
	return filepath.Clean(gitDir)
}
