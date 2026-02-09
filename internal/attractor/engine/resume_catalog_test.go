package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestResume_WithRunConfig_RequiresPerRunModelCatalogSnapshot_OpenRouterName(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	_ = os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5","supported_parameters":["tools"]}]}`), 0o644)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	_ = os.WriteFile(cli, []byte("#!/usr/bin/env bash\nset -euo pipefail\n\necho '{\"type\":\"done\",\"text\":\"ok\"}'\n"), 0o755)
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{"openai": {Backend: BackendCLI, Executable: cli}}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5, prompt="say hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "resume-modeldb", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Delete the per-run snapshot and verify resume refuses.
	_ = os.Remove(filepath.Join(logsRoot, "modeldb", "openrouter_models.json"))
	if _, err := Resume(ctx, logsRoot); err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestResume_WithRunConfig_AllowsLegacyCatalogFilenameFallback(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	_ = os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5","supported_parameters":["tools"]}]}`), 0o644)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	_ = os.WriteFile(cli, []byte("#!/usr/bin/env bash\nset -euo pipefail\n\necho '{\"type\":\"done\",\"text\":\"ok\"}'\n"), 0o755)
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{"openai": {Backend: BackendCLI, Executable: cli}}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5, prompt="say hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "resume-modeldb-legacy-fallback", LogsRoot: logsRoot, AllowTestShim: true}); err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	openrouterPath := filepath.Join(logsRoot, "modeldb", "openrouter_models.json")
	legacyPath := filepath.Join(logsRoot, "modeldb", "litellm_catalog.json")
	b, err := os.ReadFile(openrouterPath)
	if err != nil {
		t.Fatalf("read %s: %v", openrouterPath, err)
	}
	if err := os.WriteFile(legacyPath, b, 0o644); err != nil {
		t.Fatalf("write %s: %v", legacyPath, err)
	}
	if err := os.Remove(openrouterPath); err != nil {
		t.Fatalf("remove %s: %v", openrouterPath, err)
	}
	if _, err := Resume(ctx, logsRoot); err != nil {
		t.Fatalf("Resume with legacy fallback: %v", err)
	}
}
