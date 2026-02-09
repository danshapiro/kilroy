package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResumeFromCXDB_FindsLogsRootFromTurns(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	_ = os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5.2"}]}`), 0o644)

	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("#!/usr/bin/env bash\nset -euo pipefail\n\necho '{\"type\":\"done\",\"text\":\"ok\"}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{"openai": {Backend: BackendCLI, Executable: cli}}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "cxdb-run", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Get context id from manifest.json (authoritative).
	mb, err := os.ReadFile(filepath.Join(res.LogsRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var m map[string]any
	_ = json.Unmarshal(mb, &m)
	cxdbAny, _ := m["cxdb"].(map[string]any)
	ctxID := strings.TrimSpace(anyToString(cxdbAny["context_id"]))
	if ctxID == "" {
		t.Fatalf("missing context_id in manifest: %v", m["cxdb"])
	}

	res2, err := ResumeFromCXDB(ctx, cxdbSrv.URL(), ctxID)
	if err != nil {
		t.Fatalf("ResumeFromCXDB: %v", err)
	}
	if res2.RunID != res.RunID {
		t.Fatalf("run_id: got %q want %q", res2.RunID, res.RunID)
	}
}
