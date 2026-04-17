package engine

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStageStatusContract_AbsolutePaths_FromRelativeWorktreeInput(t *testing.T) {
	t.Setenv(runIDEnvKey, "test-run")
	rel := filepath.Join("tmp", "wt")
	c := BuildStageStatusContract(rel)

	if !filepath.IsAbs(c.PrimaryPath) {
		t.Fatalf("primary path must be absolute, got %q", c.PrimaryPath)
	}
	if !filepath.IsAbs(c.FallbackPath) {
		t.Fatalf("fallback path must be absolute, got %q", c.FallbackPath)
	}
}

func TestStageStatusContract_DefaultPaths(t *testing.T) {
	wt := t.TempDir()
	t.Setenv(runIDEnvKey, "test-run")
	c := BuildStageStatusContract(wt)

	if got, want := c.PrimaryPath, filepath.Join(wt, "status.json"); got != want {
		t.Fatalf("primary path: got %q want %q", got, want)
	}
	if got, want := c.FallbackPath, filepath.Join(wt, ".ai", "runs", "test-run", "status.json"); got != want {
		t.Fatalf("fallback path: got %q want %q", got, want)
	}
	if got := c.EnvVars[stageStatusPathEnvKey]; strings.TrimSpace(got) == "" {
		t.Fatalf("missing %s in EnvVars", stageStatusPathEnvKey)
	}
	if got := c.EnvVars[stageStatusFallbackPathEnvKey]; strings.TrimSpace(got) == "" {
		t.Fatalf("missing %s in EnvVars", stageStatusFallbackPathEnvKey)
	}
	if !strings.Contains(c.PromptPreamble, stageStatusPathEnvKey) {
		t.Fatalf("prompt preamble missing env key %s", stageStatusPathEnvKey)
	}
	if !strings.Contains(c.PromptPreamble, stageStatusFallbackPathEnvKey) {
		t.Fatalf("prompt preamble missing env key %s", stageStatusFallbackPathEnvKey)
	}
}
