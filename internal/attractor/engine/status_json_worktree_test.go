package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestRunWithConfig_CLIBackend_WorktreeStatusJSON_IsTreatedAsStageStatus(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

# Simulate an agent that writes status.json in its working directory (the worktree).
cat > status.json <<'JSON'
{"status":"fail","failure_reason":"synthetic failure"}
JSON

echo '{"type":"start"}'
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILROY_CODEX_PATH", cli)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]struct {
		Backend BackendKind `json:"backend" yaml:"backend"`
	}{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do the thing"]
  fix [shape=parallelogram, tool_command="echo fixed > fixed.txt"]

  start -> a
  a -> fix [condition="outcome=fail"]
  a -> exit [condition="outcome=success"]
  fix -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-status-json-worktree", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// The stage status should reflect the worktree-root status.json written by the CLI backend.
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "status.json"))
	if err != nil {
		t.Fatalf("read a/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode a/status.json: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Fatalf("a status: got %q want %q (out=%+v)", out.Status, runtime.StatusFail, out)
	}
	if strings.TrimSpace(out.FailureReason) == "" {
		t.Fatalf("a failure_reason should be non-empty (out=%+v)", out)
	}

	// Ensure the fail edge executed and produced the expected committed artifact.
	if got := strings.TrimSpace(runCmdOut(t, repo, "git", "show", res.FinalCommitSHA+":fixed.txt")); got != "fixed" {
		t.Fatalf("fixed.txt: got %q want %q", got, "fixed")
	}
}
