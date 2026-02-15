package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget verifies
// that a deterministic CLI failure (e.g. compilation error) is classified and
// retry is blocked -- the CLI should be invoked exactly once. The pipeline still
// completes via fallback edge routing (spec section 3.3) since the only edge
// (condition="outcome=success") is selected as fallback when no condition matches.
func TestRunWithConfig_CLIDeterministicFailure_DoesNotConsumeRetryBudget(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	callCountFile := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("KILROY_CALL_COUNT_FILE", callCountFile)
	codexCLI := writeDeterministicFailingCodexCLI(t)

	cfg := testOpenAICLIConfig(repo, pinned, cxdbSrv, codexCLI)
	// Graph: node "a" fails deterministically, only edge is condition="outcome=success".
	// No unconditional edge exists — routing gap after the fail. The key assertion
	// is that the CLI was invoked only once (deterministic failure blocks retry).
	dot := []byte(`
digraph G {
  graph [goal="test stage retry gate", default_max_retry="3"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider="openai", llm_model="gpt-5.2", prompt="do work", max_retries="3"]
  start -> a
  a -> exit [condition="outcome=success"]
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "retry-gate-integration", LogsRoot: logsRoot, AllowTestShim: true})
	// Pipeline may error (routing gap) or fail — either way, the key assertion
	// below is that the CLI was invoked only once.
	calls, readErr := readCallCount(callCountFile)
	if readErr != nil {
		t.Fatalf("read call count: %v; logs entries=%v", readErr, listDirNames(t, logsRoot))
	}
	if calls != 1 {
		t.Fatalf("deterministic CLI should run once, got %d", calls)
	}
	if !hasProgressEvent(t, logsRoot, "stage_retry_blocked") {
		t.Fatalf("expected stage_retry_blocked event in progress")
	}
	if hasProgressEvent(t, logsRoot, "stage_retry_sleep") {
		t.Fatalf("did not expect stage_retry_sleep for deterministic classified failure")
	}
}

func TestRunWithConfig_CLIDeterministicFailure_BlocksStageRetryAndLoopRestart_WritesTerminalFinal(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	callCountFile := filepath.Join(t.TempDir(), "calls.txt")
	t.Setenv("KILROY_CALL_COUNT_FILE", callCountFile)
	codexCLI := writeDeterministicFailingCodexCLI(t)

	cfg := testOpenAICLIConfig(repo, pinned, cxdbSrv, codexCLI)
	dot := []byte(`
digraph G {
  graph [goal="test loop restart gate", default_max_retry="3", max_restarts="5"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider="openai", llm_model="gpt-5.2", prompt="do work", max_retries="3"]
  check [shape=diamond]
  start -> a
  a -> check
  check -> exit [condition="outcome=success"]
  check -> a [condition="outcome=fail", loop_restart=true]
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "retry-loop-gate-integration", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected loop restart blocked error, got nil")
	}
	if !strings.Contains(err.Error(), "loop_restart blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
	calls, readErr := readCallCount(callCountFile)
	if readErr != nil {
		t.Fatalf("run err=%v; read call count: %v; logs entries=%v", err, readErr, listDirNames(t, logsRoot))
	}
	if calls != 1 {
		t.Fatalf("deterministic CLI should run once, got %d", calls)
	}
	if !hasProgressEvent(t, logsRoot, "stage_retry_blocked") {
		t.Fatalf("expected stage_retry_blocked event")
	}
	if hasProgressEvent(t, logsRoot, "stage_retry_sleep") {
		t.Fatalf("did not expect stage_retry_sleep for deterministic classified failure")
	}
	if !hasProgressEvent(t, logsRoot, "loop_restart_blocked") {
		t.Fatalf("expected loop_restart_blocked event")
	}

	finalPath := filepath.Join(logsRoot, "final.json")
	assertExists(t, finalPath)
	b, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read final.json: %v", err)
	}
	var final runtime.FinalOutcome
	if err := json.Unmarshal(b, &final); err != nil {
		t.Fatalf("decode final.json: %v", err)
	}
	if final.Status != runtime.FinalFail {
		t.Fatalf("final status: got %q want %q", final.Status, runtime.FinalFail)
	}
	if !strings.Contains(final.FailureReason, "failure_class=deterministic") {
		t.Fatalf("expected deterministic class block in final failure_reason, got %q", final.FailureReason)
	}
}

func testOpenAICLIConfig(repo string, pinned string, cxdbSrv *cxdbTestServer, codexCLI string) *RunConfigFile {
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: codexCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	return cfg
}

func writeDeterministicFailingCodexCLI(t *testing.T) string {
	t.Helper()
	cliPath := filepath.Join(t.TempDir(), "codex")
	script := `#!/usr/bin/env bash
set -euo pipefail

count_file="${KILROY_CALL_COUNT_FILE:?missing call count file}"
if [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
cat <<'EOF'
Usage: codex exec --json --sandbox workspace-write -m MODEL -C DIR
EOF
exit 0
fi

n=0
if [[ -f "$count_file" ]]; then
  n=$(cat "$count_file")
fi
n=$((n + 1))
echo "$n" > "$count_file"

echo "fatal: deterministic provider contract mismatch" >&2
exit 1
`
	if err := os.WriteFile(cliPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write codex fake cli: %v", err)
	}
	return cliPath
}

func readCallCount(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse call count: %w (raw=%q)", err, string(b))
	}
	return n, nil
}

func listDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{"<readdir error: " + err.Error() + ">"}
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

func mustReadOutcome(t *testing.T, path string) runtime.Outcome {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read outcome %s: %v", path, err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode outcome %s: %v", path, err)
	}
	return out
}
