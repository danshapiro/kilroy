package engine

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func writeFakeCodexHelpCLI(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(p, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
  echo "Usage: codex exec --json --sandbox workspace-write"
  exit 0
fi
echo "ok"
`), 0o755); err != nil {
		t.Fatalf("write fake codex cli: %v", err)
	}
	return p
}

func TestRunWithConfig_RealProfileRejectsShimOverrideE2E(t *testing.T) {
	repo := initTestRepo(t)
	t.Setenv("KILROY_CODEX_PATH", "/tmp/fake/codex")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "real-shim-override-e2e", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected real profile override rejection, got nil")
	}
	if !strings.Contains(err.Error(), "llm.cli_profile=real forbids provider path overrides") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithConfig_TestShimRequiresExplicitGateAndExecutable(t *testing.T) {
	repo := initTestRepo(t)
	codexCLI := writeFakeCodexHelpCLI(t)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: codexCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "shim-missing-gate", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected --allow-test-shim gate error, got nil")
	}
	if !strings.Contains(err.Error(), "--allow-test-shim") {
		t.Fatalf("unexpected gate error: %v", err)
	}

	_, err = RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "shim-with-gate", LogsRoot: t.TempDir(), AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb connectivity error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("expected preflight and policy checks to pass with explicit gate+executable, got: %v", err)
	}
}

func TestRunWithConfig_CLIBackend_StatusContractEnvInjected(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

out=""
schema=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output)
      out="$2"
      shift 2
      ;;
    --output-schema)
      schema="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [[ -n "$schema" ]]; then
  [[ -f "$schema" ]] || { echo "missing schema: $schema" >&2; exit 2; }
fi
if [[ -n "$out" ]]; then
  echo '{"final":"ok","summary":"ok"}' > "$out"
fi

[[ -n "${KILROY_STAGE_STATUS_PATH:-}" ]] || { echo "missing KILROY_STAGE_STATUS_PATH" >&2; exit 31; }
[[ -n "${KILROY_STAGE_STATUS_FALLBACK_PATH:-}" ]] || { echo "missing KILROY_STAGE_STATUS_FALLBACK_PATH" >&2; exit 32; }
[[ "${KILROY_STAGE_STATUS_PATH}" = /* ]] || { echo "status path must be absolute" >&2; exit 33; }
[[ "${KILROY_STAGE_STATUS_FALLBACK_PATH}" = /* ]] || { echo "fallback path must be absolute" >&2; exit 34; }

mkdir -p "$(dirname "$KILROY_STAGE_STATUS_PATH")"
cat > "$KILROY_STAGE_STATUS_PATH" <<'JSON'
{"status":"success","notes":"status-contract-env"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="status contract env injected"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "status-contract-env-injected", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	statusBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "status.json"))
	if err != nil {
		t.Fatalf("read a/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(statusBytes)
	if err != nil {
		t.Fatalf("decode a/status.json: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("a status: got %q want %q (out=%+v)", out.Status, runtime.StatusSuccess, out)
	}

	var inv map[string]any
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	if err != nil {
		t.Fatalf("read cli_invocation.json: %v", err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal cli_invocation.json: %v", err)
	}
	gotStatusPath := strings.TrimSpace(anyToString(inv["status_path"]))
	if gotStatusPath == "" {
		t.Fatalf("cli_invocation.status_path missing: %#v", inv)
	}
	if !filepath.IsAbs(gotStatusPath) {
		t.Fatalf("cli_invocation.status_path must be absolute: %q", gotStatusPath)
	}
	if gotStatusPath != filepath.Join(res.WorktreeDir, "status.json") {
		t.Fatalf("cli_invocation.status_path: got %q want %q", gotStatusPath, filepath.Join(res.WorktreeDir, "status.json"))
	}
	gotFallbackPath := strings.TrimSpace(anyToString(inv["status_fallback_path"]))
	if gotFallbackPath != filepath.Join(res.WorktreeDir, ".ai", "status.json") {
		t.Fatalf("cli_invocation.status_fallback_path: got %q want %q", gotFallbackPath, filepath.Join(res.WorktreeDir, ".ai", "status.json"))
	}
	if gotEnvKey := strings.TrimSpace(anyToString(inv["status_env_key"])); gotEnvKey != stageStatusPathEnvKey {
		t.Fatalf("cli_invocation.status_env_key: got %q want %q", gotEnvKey, stageStatusPathEnvKey)
	}
}

func TestRunWithConfig_CLIBackend_CapturesInvocationAndPersistsArtifactsToCXDB(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

out=""
schema=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o)
      out="$2"
      shift 2
      ;;
    --output-schema)
      schema="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -n "$schema" ]]; then
  [[ -f "$schema" ]] || { echo "missing schema: $schema" >&2; exit 2; }
fi
if [[ -n "$out" ]]; then
  echo '{"final":"ok","summary":"ok"}' > "$out"
fi

echo 'from_cli' > cli_wrote.txt

# Simulate an agent producing a status.json in its working directory (the worktree).
cat > status.json <<'JSON'
{"status":"success","notes":"from_cli"}
JSON

echo '{"type":"start"}'
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	seedHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(seedHome, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedHome, ".codex", "auth.json"), []byte(`{"token":"seeded"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seedHome, ".codex", "config.toml"), []byte(`model = "gpt-5"`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", seedHome)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
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
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-run-cli", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// CLI artifacts captured.
	assertExists(t, filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "stdout.log"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "events.ndjson"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "events.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "output_schema.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "output.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "stage.tgz"))
	assertExists(t, filepath.Join(res.LogsRoot, "run.tgz"))

	stageEntries := listTarGzEntries(t, filepath.Join(res.LogsRoot, "a", "stage.tgz"))
	assertNoCodexStateEntries(t, stageEntries)
	runEntries := listTarGzEntries(t, filepath.Join(res.LogsRoot, "run.tgz"))
	assertNoCodexStateEntries(t, runEntries)

	// Invocation includes env capture fields (metaspec replayability).
	var inv map[string]any
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	if err != nil {
		t.Fatalf("read cli_invocation.json: %v", err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal cli_invocation.json: %v", err)
	}
	if inv["env_mode"] != "isolated" {
		t.Fatalf("env_mode: got %v want isolated", inv["env_mode"])
	}
	if strings.TrimSpace(anyToString(inv["env_scope"])) != "codex" {
		t.Fatalf("env_scope: %#v", inv["env_scope"])
	}
	stateRoot := strings.TrimSpace(anyToString(inv["state_root"]))
	if stateRoot == "" {
		t.Fatalf("state_root missing: %#v", inv)
	}
	assertExists(t, filepath.Join(stateRoot, "auth.json"))
	assertExists(t, filepath.Join(stateRoot, "config.toml"))
	if strings.HasPrefix(stateRoot, filepath.Clean(res.LogsRoot)+string(filepath.Separator)) || stateRoot == filepath.Clean(res.LogsRoot) {
		t.Fatalf("state_root should be outside logs root: logs_root=%q state_root=%q", res.LogsRoot, stateRoot)
	}
	if strings.TrimSpace(anyToString(inv["working_dir"])) == "" {
		t.Fatalf("working_dir missing in invocation: %#v", inv["working_dir"])
	}
	if strings.TrimSpace(anyToString(inv["structured_output_path"])) == "" {
		t.Fatalf("structured_output_path missing in invocation: %#v", inv["structured_output_path"])
	}
	if strings.TrimSpace(anyToString(inv["structured_output_schema_path"])) == "" {
		t.Fatalf("structured_output_schema_path missing in invocation: %#v", inv["structured_output_schema_path"])
	}

	// Confirm CLI ran in the worktree by checking the committed file exists.
	if got := strings.TrimSpace(runCmdOut(t, repo, "git", "show", res.FinalCommitSHA+":cli_wrote.txt")); got != "from_cli" {
		t.Fatalf("cli_wrote.txt: got %q want %q", got, "from_cli")
	}

	// CXDB received normalized turns and artifact turns.
	manifestBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest.json: %v", err)
	}
	var manifest map[string]any
	_ = json.Unmarshal(manifestBytes, &manifest)
	cxdbInfo, _ := manifest["cxdb"].(map[string]any)
	ctxID := ""
	if cxdbInfo != nil {
		ctxID = strings.TrimSpace(anyToString(cxdbInfo["context_id"]))
	}
	if ctxID == "" {
		t.Fatalf("manifest missing cxdb.context_id: %v", manifest["cxdb"])
	}
	modeldbInfo, _ := manifest["modeldb"].(map[string]any)
	if strings.TrimSpace(anyToString(modeldbInfo["openrouter_model_info_path"])) == "" {
		t.Fatalf("manifest missing modeldb.openrouter_model_info_path: %v", manifest["modeldb"])
	}

	turns := cxdbSrv.Turns(ctxID)
	if len(turns) == 0 {
		t.Fatalf("expected cxdb turns, got 0")
	}
	hasRunStarted := false
	hasRunStartedLogsRoot := false
	hasRunStartedGraphDot := false
	hasPrompt := false
	var promptText string
	for _, tr := range turns {
		if tr["type_id"] == "com.kilroy.attractor.RunStarted" {
			hasRunStarted = true
			if p, ok := tr["payload"].(map[string]any); ok {
				if strings.TrimSpace(anyToString(p["logs_root"])) == strings.TrimSpace(res.LogsRoot) {
					hasRunStartedLogsRoot = true
				}
				if strings.TrimSpace(anyToString(p["graph_dot"])) != "" {
					hasRunStartedGraphDot = true
				}
			}
		}
		if tr["type_id"] == "com.kilroy.attractor.Prompt" {
			hasPrompt = true
			if p, ok := tr["payload"].(map[string]any); ok {
				promptText = anyToString(p["text"])
			}
		}
		// Plumbing turns must not appear in CXDB (files still on disk).
		if tr["type_id"] == "com.kilroy.attractor.Artifact" {
			t.Fatalf("unexpected Artifact turn in CXDB chain")
		}
		if tr["type_id"] == "com.kilroy.attractor.GitCheckpoint" {
			t.Fatalf("unexpected GitCheckpoint turn in CXDB chain")
		}
		if tr["type_id"] == "com.kilroy.attractor.CheckpointSaved" {
			t.Fatalf("unexpected CheckpointSaved turn in CXDB chain")
		}
	}
	if !hasRunStarted {
		t.Fatalf("expected RunStarted turn")
	}
	if !hasRunStartedLogsRoot {
		t.Fatalf("expected RunStarted.logs_root to equal logs_root=%q", res.LogsRoot)
	}
	if !hasRunStartedGraphDot {
		t.Fatalf("expected RunStarted.graph_dot to contain .dot file content")
	}
	if !hasPrompt {
		t.Fatalf("expected Prompt turn for orchestrator-to-agent prompt")
	}
	if strings.TrimSpace(promptText) == "" {
		t.Fatalf("expected Prompt turn text to be non-empty")
	}

	// final.json includes CXDB context + head turn id (metaspec).
	finalBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "final.json"))
	if err != nil {
		t.Fatalf("read final.json: %v", err)
	}
	var final map[string]any
	_ = json.Unmarshal(finalBytes, &final)
	if strings.TrimSpace(anyToString(final["cxdb_context_id"])) == "" || strings.TrimSpace(anyToString(final["cxdb_head_turn_id"])) == "" {
		t.Fatalf("final.json missing cxdb ids: %v", final)
	}
}

func TestRunWithConfig_APIBackend_AgentLoop_WritesAgentEventsAndPassesReasoningEffort(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	gotReasoningLow := false
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var gotReq map[string]any
		_ = json.Unmarshal(b, &gotReq)
		if reasoningAny, ok := gotReq["reasoning"].(map[string]any); ok {
			if strings.TrimSpace(anyToString(reasoningAny["effort"])) == "low" {
				mu.Lock()
				gotReasoningLow = true
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, reasoning_effort=low, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-run-api-agent-loop", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Agent-loop artifacts captured (metaspec).
	assertExists(t, filepath.Join(res.LogsRoot, "a", "events.ndjson"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "events.json"))

	mu.Lock()
	ok := gotReasoningLow
	mu.Unlock()
	if !ok {
		t.Fatalf("expected at least one OpenAI request to include reasoning.effort=low")
	}
}

func TestRunWithConfig_APIBackend_OneShot_WritesRequestAndResponseArtifacts(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, codergen_mode=one_shot, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-run-api-oneshot", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	assertExists(t, filepath.Join(res.LogsRoot, "a", "api_request.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "api_response.json"))
}

func TestRunWithConfig_APIBackend_ForceModelOverride_UsesForcedModel(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	gotModel := ""
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var gotReq map[string]any
		_ = json.Unmarshal(b, &gotReq)
		mu.Lock()
		gotModel = strings.TrimSpace(anyToString(gotReq["model"]))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-unknown-force-b",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-unknown-dot-a, codergen_mode=one_shot, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "test-run-api-force-model",
		LogsRoot:    logsRoot,
		ForceModels: map[string]string{"openai": "gpt-unknown-force-b"},
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	assertExists(t, filepath.Join(res.LogsRoot, "a", "api_request.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "api_response.json"))

	mu.Lock()
	model := gotModel
	mu.Unlock()
	if model != "gpt-unknown-force-b" {
		t.Fatalf("openai request model: got %q want %q", model, "gpt-unknown-force-b")
	}
}

func TestRunWithConfig_APIBackend_AutoStatusFalse_FailsWhenNoStatusWritten(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, codergen_mode=one_shot, prompt="say hi"]
  fix [shape=parallelogram, tool_command="echo fixed > fixed.txt"]

  start -> a
  a -> fix  [condition="outcome=fail"]
  a -> exit [condition="outcome=success"]
  fix -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-run-api-autostatus-false", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// API backend does not produce a status.json signal by itself; without auto_status=true,
	// codergen must fail to preserve the contract.
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
	if !strings.Contains(out.FailureReason, "missing status.json") {
		t.Fatalf("a failure_reason: got %q want to mention missing status.json", out.FailureReason)
	}

	// Ensure the fail edge executed and produced the expected committed artifact.
	if got := strings.TrimSpace(runCmdOut(t, repo, "git", "show", res.FinalCommitSHA+":fixed.txt")); got != "fixed" {
		t.Fatalf("fixed.txt: got %q want %q", got, "fixed")
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")
	return repo
}

func writePinnedCatalog(t *testing.T) string {
	t.Helper()
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"openai/gpt-5.2"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return pinned
}

func listTarGzEntries(t *testing.T, tarPath string) []string {
	t.Helper()
	f, err := os.Open(tarPath)
	if err != nil {
		t.Fatalf("open tar %s: %v", tarPath, err)
	}
	defer func() { _ = f.Close() }()

	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader %s: %v", tarPath, err)
	}
	defer func() { _ = gz.Close() }()

	tr := tar.NewReader(gz)
	entries := []string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return entries
		}
		if err != nil {
			t.Fatalf("tar read %s: %v", tarPath, err)
		}
		entries = append(entries, h.Name)
	}
}

func assertNoCodexStateEntries(t *testing.T, entries []string) {
	t.Helper()
	for _, name := range entries {
		n := filepath.ToSlash(strings.TrimSpace(name))
		if strings.HasPrefix(n, "codex-home/") || n == "codex-home" {
			t.Fatalf("unexpected codex state entry in tar: %q", name)
		}
		if strings.Contains(n, "/codex-home/") || strings.HasSuffix(n, "/codex-home") {
			t.Fatalf("unexpected codex state entry in tar: %q", name)
		}
		if strings.HasSuffix(n, "/.codex/auth.json") || strings.HasSuffix(n, "/.codex/config.toml") {
			t.Fatalf("unexpected codex credential entry in tar: %q", name)
		}
	}
}
