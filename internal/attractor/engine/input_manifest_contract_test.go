package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestInputManifestContract_AgentEnvAndPreambleInjected(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	mustWriteInputFile(t, filepath.Join(repo, ".ai", "definition_of_done.md"), "line by line")

	cli := writeInputManifestProbeCLI(t, false)
	cfg := newInputManifestContractConfig(t, repo, cli)

	dot := []byte(`
digraph G {
  graph [goal="manifest contract"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="check inputs"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "manifest-contract-enabled",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
		DisableCXDB:   true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	envPath := filepath.Join(res.LogsRoot, "a", "inputs_manifest_env.txt")
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read %s: %v", envPath, err)
	}
	manifestPath := strings.TrimSpace(string(b))
	if manifestPath == "" {
		t.Fatal("expected KILROY_INPUTS_MANIFEST_PATH to be set")
	}
	if !filepath.IsAbs(manifestPath) {
		t.Fatalf("manifest path must be absolute, got %q", manifestPath)
	}
	assertExists(t, manifestPath)
	assertExists(t, inputRunManifestPath(res.LogsRoot))
	assertExists(t, inputStageManifestPath(res.LogsRoot, "a"))

	promptPath := filepath.Join(res.LogsRoot, "a", "prompt.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt.md: %v", err)
	}
	if !strings.Contains(string(prompt), "Input materialization contract") {
		t.Fatalf("prompt missing input materialization preamble: %s", promptPath)
	}
}

func TestInputManifestContract_DisabledSuppressesEnvPreambleAndEvents(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	t.Setenv(inputsManifestEnvKey, "/tmp/ambient-inputs-manifest.json")
	mustWriteInputFile(t, filepath.Join(repo, ".ai", "definition_of_done.md"), "line by line")

	cli := writeInputManifestProbeCLI(t, false)
	cfg := newInputManifestContractConfig(t, repo, cli)
	enabled := false
	cfg.Inputs.Materialize.Enabled = &enabled

	dot := []byte(`
digraph G {
  graph [goal="manifest contract disabled"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="check inputs"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "manifest-contract-disabled",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
		DisableCXDB:   true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	envPath := filepath.Join(res.LogsRoot, "a", "inputs_manifest_env.txt")
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("read %s: %v", envPath, err)
	}
	if strings.TrimSpace(string(b)) != "" {
		t.Fatalf("expected empty KILROY_INPUTS_MANIFEST_PATH when disabled, got %q", strings.TrimSpace(string(b)))
	}
	if _, err := os.Stat(inputRunManifestPath(res.LogsRoot)); !os.IsNotExist(err) {
		t.Fatalf("run manifest should not exist when disabled, stat err=%v", err)
	}
	if _, err := os.Stat(inputStageManifestPath(res.LogsRoot, "a")); !os.IsNotExist(err) {
		t.Fatalf("stage manifest should not exist when disabled, stat err=%v", err)
	}

	promptPath := filepath.Join(res.LogsRoot, "a", "prompt.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt.md: %v", err)
	}
	if strings.Contains(string(prompt), "Input materialization contract") {
		t.Fatalf("prompt should not include input materialization preamble when disabled: %s", promptPath)
	}

	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	progress, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}
	if strings.Contains(string(progress), "input_materialization_") {
		t.Fatalf("progress should not contain input materialization events when disabled")
	}
}

func TestInputManifestContract_FollowReferencesFalseSkipsRecursiveClosure(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	mustWriteInputFile(t, filepath.Join(repo, ".ai", "definition_of_done.md"), "See [tests](../tests.md)")
	mustWriteInputFile(t, filepath.Join(repo, "tests.md"), "tests")

	cfg := newInputMaterializationRunConfigForTest(t, repo)
	follow := false
	cfg.Inputs.Materialize.FollowReferences = &follow

	dot := []byte(`
digraph G {
  graph [goal="follow references false"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  verify [shape=parallelogram, tool_command="test -f .ai/definition_of_done.md && test ! -f tests.md"]
  start -> verify -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "follow-ref-false", LogsRoot: logsRoot, DisableCXDB: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	progress, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}
	if strings.Contains(strings.ToLower(string(progress)), "input inference failed") {
		t.Fatalf("infer_with_llm=false should not emit inference warning")
	}
}

func TestInputManifestContract_LineageRevisionFields(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	cfg := newInputMaterializationRunConfigForTest(t, repo)
	runID := "lineage-manifest-fields"

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, branchIsolationDOTForRunID(runID), cfg, RunOptions{
		RunID:       runID,
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	results := mustLoadParallelResults(t, filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	branchResult := mustParallelResultByStartNode(t, results, "a")

	branchManifest := mustLoadInputManifest(t, inputRunManifestPath(branchResult.LogsRoot))
	if strings.TrimSpace(branchManifest.BaseRunRevision) == "" || strings.TrimSpace(branchManifest.BranchHeadRevision) == "" {
		t.Fatalf("branch manifest missing base/head revisions: %+v", branchManifest)
	}

	branchStageManifest := mustLoadInputManifest(t, inputStageManifestPath(branchResult.LogsRoot, "a"))
	if strings.TrimSpace(branchStageManifest.RunBaseRevision) == "" || strings.TrimSpace(branchStageManifest.BranchRevision) == "" {
		t.Fatalf("branch stage manifest missing lineage tuple: %+v", branchStageManifest)
	}
}

func newInputManifestContractConfig(t *testing.T, repo string, cliPath string) *RunConfigFile {
	t.Helper()
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {
			Backend:    BackendCLI,
			Executable: cliPath,
		},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	requireClean := false
	cfg.Git.RequireClean = &requireClean
	enabled := true
	follow := true
	infer := false
	cfg.Inputs.Materialize.Enabled = &enabled
	cfg.Inputs.Materialize.DefaultInclude = []string{".ai/**"}
	cfg.Inputs.Materialize.FollowReferences = &follow
	cfg.Inputs.Materialize.InferWithLLM = &infer
	return cfg
}

func writeInputManifestProbeCLI(t *testing.T, requireInputManifest bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "codex")
	requireLine := ""
	if requireInputManifest {
		requireLine = `[[ -n "${KILROY_INPUTS_MANIFEST_PATH:-}" ]] || { echo "missing KILROY_INPUTS_MANIFEST_PATH" >&2; exit 41; }`
	}
	script := `#!/usr/bin/env bash
set -euo pipefail

out=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o|--output)
      out="$2"
      shift 2
      ;;
    --output-schema)
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done

[[ -n "${KILROY_STAGE_STATUS_PATH:-}" ]] || { echo "missing KILROY_STAGE_STATUS_PATH" >&2; exit 31; }
[[ -n "${KILROY_STAGE_LOGS_DIR:-}" ]] || { echo "missing KILROY_STAGE_LOGS_DIR" >&2; exit 32; }
` + requireLine + `

echo "${KILROY_INPUTS_MANIFEST_PATH:-}" > "${KILROY_STAGE_LOGS_DIR}/inputs_manifest_env.txt"
if [[ -n "${KILROY_INPUTS_MANIFEST_PATH:-}" ]]; then
  [[ -f "${KILROY_INPUTS_MANIFEST_PATH}" ]] || { echo "manifest path missing" >&2; exit 42; }
fi
if [[ -n "$out" ]]; then
  echo '{"ok":"yes"}' > "$out"
fi
cat > "$KILROY_STAGE_STATUS_PATH" <<'JSON'
{"status":"success","notes":"manifest probe"}
JSON
echo '{"type":"done","text":"ok"}'
`
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write probe cli: %v", err)
	}
	return p
}
