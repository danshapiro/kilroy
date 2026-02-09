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

func TestRunWithConfig_ParallelBranches_ForkCXDBContexts(t *testing.T) {
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
	if err := os.WriteFile(cli, []byte("#!/usr/bin/env bash\nset -euo pipefail\n\ncat > status.json <<'JSON'\n{\"status\":\"success\",\"notes\":\"ok\"}\nJSON\n\necho '{\"type\":\"done\",\"text\":\"ok\"}'\n"), 0o755); err != nil {
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
digraph P {
  graph [goal="test"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  join [shape=tripleoctagon]
  exit [shape=Msquare]

  start -> par
  par -> a
  par -> b
  a -> join
  b -> join
  join -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "parallel-cxdb", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Expect more than one context: main + fork(s).
	if n := len(cxdbSrv.ContextIDs()); n <= 1 {
		t.Fatalf("expected cxdb context forks; got %d contexts", n)
	}

	// parallel_results.json should include per-branch cxdb_context_id.
	prPath := filepath.Join(res.LogsRoot, "par", "parallel_results.json")
	bs, err := os.ReadFile(prPath)
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(bs, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 branch results, got %d", len(results))
	}
	for _, r := range results {
		if strings.TrimSpace(anyToString(r["cxdb_context_id"])) == "" {
			t.Fatalf("missing cxdb_context_id in branch result: %v", r)
		}
	}
}
