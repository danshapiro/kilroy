package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestDefaultCodexOutputSchema_AllowsAdditionalPropertiesAndRequiresCoreFields(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(defaultCodexOutputSchema), &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	if got, ok := schema["additionalProperties"].(bool); !ok || !got {
		t.Fatalf("additionalProperties: got %#v want true", schema["additionalProperties"])
	}
	req, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required should be an array: %#v", schema["required"])
	}
	want := map[string]bool{"final": true, "summary": true}
	if len(req) != len(want) {
		t.Fatalf("required length: got %d want %d (%#v)", len(req), len(want), req)
	}
	for _, v := range req {
		s, _ := v.(string)
		if !want[s] {
			t.Fatalf("unexpected required key %q (all=%#v)", s, req)
		}
	}
}

func TestRunWithConfig_CLIBackend_OpenAISchemaFallback(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

schema=""
out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-schema)
      schema="$2"
      shift 2
      ;;
    -o|--output)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -n "$schema" ]]; then
  echo 'invalid_json_schema: unsupported additionalProperties' >&2
  exit 1
fi

if [[ -n "$out" ]]; then
  echo '{"final":"ok","summary":"ok"}' > "$out"
fi

cat > status.json <<'JSON'
{"status":"success","notes":"fallback path"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILROY_CODEX_PATH", cli)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test schema fallback"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "schema-fallback", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	assertExists(t, filepath.Join(res.LogsRoot, "a", "stdout.schema_failure.log"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "stderr.schema_failure.log"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "stdout.log"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "status.json"))

	var inv map[string]any
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	if err != nil {
		t.Fatalf("read cli_invocation.json: %v", err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal cli_invocation.json: %v", err)
	}
	retried, _ := inv["schema_fallback_retry"].(bool)
	if !retried {
		t.Fatalf("expected schema_fallback_retry=true in invocation: %#v", inv)
	}
}

func TestRunWithConfig_CLIBackend_OpenAIStateDBFallbackRetry(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	sentinel := filepath.Join(t.TempDir(), "first-attempt-sentinel")
	t.Setenv("KILROY_TEST_STATE_DB_SENTINEL", sentinel)
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

marker="${KILROY_TEST_STATE_DB_SENTINEL:?missing marker}"
if [[ ! -f "$marker" ]]; then
  touch "$marker"
  echo 'state db missing rollout path for thread test-thread' >&2
  exit 1
fi

if [[ -n "$out" ]]; then
  echo '{"final":"ok","summary":"ok"}' > "$out"
fi
cat > status.json <<'JSON'
{"status":"success","notes":"state-db retry success"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KILROY_CODEX_PATH", cli)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test state db fallback"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "state-db-fallback", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	assertExists(t, filepath.Join(res.LogsRoot, "a", "status.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "output.json"))

	var inv map[string]any
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	if err != nil {
		t.Fatalf("read cli_invocation.json: %v", err)
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		t.Fatalf("unmarshal cli_invocation.json: %v", err)
	}
	retried, _ := inv["state_db_fallback_retry"].(bool)
	if !retried {
		t.Fatalf("expected state_db_fallback_retry=true in invocation: %#v", inv)
	}
}

func TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeysTriggersLoudFallback(t *testing.T) {
	res, inv, unknown, err := runOpenAIUnknownKeysFallback(t)
	if err != nil {
		t.Fatalf("runOpenAIUnknownKeysFallback: %v", err)
	}
	firstArgs := asStringSlice(inv["argv"])
	if !sliceContains(firstArgs, "--output-schema") {
		t.Fatalf("first invocation argv must include --output-schema: %v", firstArgs)
	}
	retryArgs := asStringSlice(inv["argv_schema_retry"])
	if len(retryArgs) == 0 {
		t.Fatalf("expected argv_schema_retry metadata to be populated: %#v", inv)
	}
	if sliceContains(retryArgs, "--output-schema") {
		t.Fatalf("retry argv should omit --output-schema: %v", retryArgs)
	}
	retried, _ := inv["schema_fallback_retry"].(bool)
	if !retried {
		t.Fatalf("expected schema_fallback_retry=true in invocation: %#v", inv)
	}
	if got := strings.TrimSpace(anyToString(inv["schema_fallback_reason"])); got != "unknown_structured_keys" {
		t.Fatalf("schema_fallback_reason: got %q want %q", got, "unknown_structured_keys")
	}
	keys := asStringSlice(inv["structured_output_unknown_keys"])
	if len(keys) != 1 || keys[0] != "unexpected_extra_key" {
		t.Fatalf("structured_output_unknown_keys: got %v want [unexpected_extra_key]", keys)
	}
	if unknown == nil {
		t.Fatalf("structured_output_unknown_keys.json not parsed")
	}
	if got := asStringSlice(unknown["unknown_keys"]); len(got) != 1 || got[0] != "unexpected_extra_key" {
		t.Fatalf("unknown artifact unknown_keys: got %v", got)
	}
	warnings := strings.Join(res.Warnings, "\n")
	if !strings.Contains(warnings, "structured output has unknown keys") {
		t.Fatalf("expected warning about unknown structured output keys; warnings=%v", res.Warnings)
	}
}

func TestRunWithConfig_CLIBackend_OpenAIStructuredOutput_UnknownKeysStillAllowsSuccess(t *testing.T) {
	res, inv, unknown, err := runOpenAIUnknownKeysFallback(t)
	if err != nil {
		t.Fatalf("runOpenAIUnknownKeysFallback: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %s want %s", res.FinalStatus, runtime.FinalSuccess)
	}
	if len(asStringSlice(inv["argv_schema_retry"])) == 0 {
		t.Fatalf("expected schema fallback retry metadata: %#v", inv)
	}
	if unknown == nil {
		t.Fatalf("expected structured_output_unknown_keys artifact")
	}
	outBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "output.json"))
	if err != nil {
		t.Fatalf("read output.json: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(outBytes, &out); err != nil {
		t.Fatalf("unmarshal output.json: %v", err)
	}
	if got := strings.TrimSpace(anyToString(out["final"])); got != "ok" {
		t.Fatalf("output.final: got %q want %q", got, "ok")
	}
	if got := strings.TrimSpace(anyToString(out["summary"])); got != "ok" {
		t.Fatalf("output.summary: got %q want %q", got, "ok")
	}
	if _, has := out["unexpected_extra_key"]; has {
		t.Fatalf("fallback output.json should be normalized from retry: %#v", out)
	}
}

func runOpenAIUnknownKeysFallback(t *testing.T) (*Result, map[string]any, map[string]any, error) {
	t.Helper()
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail

schema=""
out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output-schema)
      schema="$2"
      shift 2
      ;;
    -o|--output)
      out="$2"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

if [[ -n "$out" ]]; then
  if [[ -n "$schema" ]]; then
    echo '{"final":"ok","summary":"ok","unexpected_extra_key":"surprise"}' > "$out"
  else
    echo '{"final":"ok","summary":"ok"}' > "$out"
  fi
fi

cat > status.json <<'JSON'
{"status":"success","notes":"unknown-keys-fallback"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		return nil, nil, nil, err
	}
	t.Setenv("KILROY_CODEX_PATH", cli)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test structured unknown-key fallback"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "unknown-keys-fallback", LogsRoot: logsRoot})
	if err != nil {
		return nil, nil, nil, err
	}

	var inv map[string]any
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "a", "cli_invocation.json"))
	if err != nil {
		return nil, nil, nil, err
	}
	if err := json.Unmarshal(b, &inv); err != nil {
		return nil, nil, nil, err
	}

	unknownPath := filepath.Join(res.LogsRoot, "a", "structured_output_unknown_keys.json")
	var unknown map[string]any
	if ub, err := os.ReadFile(unknownPath); err == nil {
		if uerr := json.Unmarshal(ub, &unknown); uerr != nil {
			return nil, nil, nil, uerr
		}
	}

	return res, inv, unknown, nil
}

func asStringSlice(v any) []string {
	if v == nil {
		return nil
	}
	switch typed := v.(type) {
	case []string:
		return append([]string{}, typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, strings.TrimSpace(anyToString(item)))
		}
		return out
	default:
		return nil
	}
}

func sliceContains(items []string, want string) bool {
	for _, item := range items {
		if strings.TrimSpace(item) == strings.TrimSpace(want) {
			return true
		}
	}
	return false
}
