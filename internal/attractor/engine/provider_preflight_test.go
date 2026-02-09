package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type preflightReportDoc struct {
	CLIProfile    string `json:"cli_profile"`
	AllowTestShim bool   `json:"allow_test_shim"`
	Summary       struct {
		Pass int `json:"pass"`
		Warn int `json:"warn"`
		Fail int `json:"fail"`
	} `json:"summary"`
	Checks []struct {
		Name     string         `json:"name"`
		Provider string         `json:"provider"`
		Status   string         `json:"status"`
		Message  string         `json:"message"`
		Details  map[string]any `json:"details"`
	} `json:"checks"`
}

func TestRunProviderCapabilityProbe_TimesOutAndKillsProcessGroup(t *testing.T) {
	parentPIDPath := filepath.Join(t.TempDir(), "parent.pid")
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	cliPath := writeBlockingProbeCLI(t, "gemini", parentPIDPath, childPIDPath)

	_, err := runProviderCapabilityProbe(context.Background(), "google", cliPath)
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "probe timed out after 3s") {
		t.Fatalf("unexpected timeout error: %v", err)
	}

	parentPID := mustReadPIDFile(t, parentPIDPath)
	childPID := mustReadPIDFile(t, childPIDPath)
	waitForPIDToExit(t, parentPID, 5*time.Second)
	waitForPIDToExit(t, childPID, 5*time.Second)
}

func TestRunProviderCapabilityProbe_RespectsParentContextCancel(t *testing.T) {
	parentPIDPath := filepath.Join(t.TempDir(), "parent.pid")
	childPIDPath := filepath.Join(t.TempDir(), "child.pid")
	cliPath := writeBlockingProbeCLI(t, "gemini", parentPIDPath, childPIDPath)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(250 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := runProviderCapabilityProbe(ctx, "google", cliPath)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("expected cancellation error, got nil")
	}
	if !strings.Contains(err.Error(), "probe canceled") {
		t.Fatalf("unexpected cancel error: %v", err)
	}
	if elapsed >= 3*time.Second {
		t.Fatalf("probe should stop on parent cancellation before timeout; elapsed=%s", elapsed)
	}

	parentPID := mustReadPIDFile(t, parentPIDPath)
	childPID := mustReadPIDFile(t, childPIDPath)
	waitForPIDToExit(t, parentPID, 5*time.Second)
	waitForPIDToExit(t, childPID, 5*time.Second)
}

func TestRunWithConfig_FailsFast_WhenCLIModelNotInCatalogForProvider(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	dot := singleProviderDot("google", "gemini-3-pro")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-fail", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected preflight error, got nil")
	}
	want := "preflight: llm_provider=google backend=cli model=gemini-3-pro not present in run catalog"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected preflight error containing %q, got %v", want, err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected preflight report with failure summary, got %+v", report.Summary)
	}
}

func TestRunWithConfig_FailsFast_WhenAPIModelNotInCatalogForProvider(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-opus-4-6"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendAPI,
	})
	dot := singleProviderDot("openai", "gpt-5.3-codex")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-api-fail", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected preflight error, got nil")
	}
	want := "preflight: llm_provider=openai backend=api model=gpt-5.3-codex not present in run catalog"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected preflight error containing %q, got %v", want, err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected preflight report with failure summary, got %+v", report.Summary)
	}
}

func TestRunWithConfig_ForceModel_BypassesCatalogGate(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = catalog
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := singleProviderDot("openai", "gpt-5.3-codex")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "preflight-force-model-bypass",
		LogsRoot:    logsRoot,
		ForceModels: map[string]string{"openai": "gpt-5.3-codex"},
	})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight: llm_provider=openai backend=api model=gpt-5.3-codex not present in run catalog") {
		t.Fatalf("force-model should bypass provider/model catalog gate, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected no preflight failures with force-model, got %+v", report.Summary)
	}
}

func TestRunWithConfig_UsesModelFallbackAttributeForCatalogValidation(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	dot := singleProviderModelAttrDot("google", "gemini-3-pro")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-model-attr-fail", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected preflight error, got nil")
	}
	want := "preflight: llm_provider=google backend=cli model=gemini-3-pro not present in run catalog"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("expected preflight error containing %q, got %v", want, err)
	}
}

func TestRunWithConfig_AllowsCLIModel_WhenCatalogHasProviderMatch(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLI(t, "gemini", "Usage: gemini -p --output-format stream-json --yolo --approval-mode", 0)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-pass", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream error after preflight (cxdb is intentionally unreachable), got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("expected preflight to pass for provider/model in catalog, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected no preflight failures, got %+v", report.Summary)
	}
}

func TestRunWithConfig_PreflightFails_WhenGoogleModelProbeReportsModelNotFound(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLIWithModelProbeFailure(
		t,
		"gemini",
		"Usage: gemini -p --output-format stream-json --yolo --approval-mode",
		0,
		"gemini-3-pro-preview",
		"ModelNotFoundError: Requested entity was not found.",
		1,
	)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-model-not-found", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected model probe preflight error, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider google model probe failed for model gemini-3-pro-preview: model not available") {
		t.Fatalf("unexpected error: %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected failed preflight model-access check, got %+v", report.Summary)
	}
}

func TestRunWithConfig_PreflightModelProbeFailure_WarnsWhenNonStrict(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLIWithModelProbeFailure(
		t,
		"gemini",
		"Usage: gemini -p --output-format stream-json --yolo --approval-mode",
		0,
		"gemini-3-pro-preview",
		"temporary backend issue",
		1,
	)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-model-warn", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight: provider google model probe failed") {
		t.Fatalf("expected non-strict model probe failure to warn and continue, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Warn == 0 {
		t.Fatalf("expected warning in preflight report, got %+v", report.Summary)
	}
}

func TestRunWithConfig_PreflightFails_WhenProviderCLIBinaryMissing(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	missingGemini := filepath.Join(t.TempDir(), "does-not-exist")

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: missingGemini}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-binary-missing", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected preflight binary-missing error, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider google cli binary not found") {
		t.Fatalf("unexpected error: %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected preflight report fail count > 0, got %+v", report.Summary)
	}
}

func TestRunWithConfig_PreflightFails_WhenAnthropicCapabilityMissingVerbose(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)
	claudeCLI := writeFakeCLI(t, "claude", "Usage: claude -p --output-format stream-json --model MODEL", 0)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"anthropic": BackendCLI,
	})
	cfg.LLM.Providers["anthropic"] = ProviderConfig{Backend: BackendCLI, Executable: claudeCLI}
	dot := singleProviderDot("anthropic", "claude-sonnet-4-20250514")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-anthropic-verbose", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected anthropic capability error, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider anthropic capability probe missing required tokens") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "--verbose") {
		t.Fatalf("expected missing --verbose token in error, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected preflight report fail count > 0, got %+v", report.Summary)
	}
}

func TestRunWithConfig_WritesPreflightReport_Always(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLI(t, "gemini", "Usage: gemini -p --output-format stream-json --yolo", 0)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-report-always", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	report := mustReadPreflightReport(t, logsRoot)
	if len(report.Checks) == 0 {
		t.Fatalf("expected non-empty preflight checks")
	}
}

func TestRunWithConfig_PreflightCapabilityProbeFailure_WarnsWhenNonStrict(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLI(t, "gemini", "probe error", 2)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-warn-nonstrict", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("expected non-strict probe failure to warn and continue, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Warn == 0 {
		t.Fatalf("expected warning in preflight report, got %+v", report.Summary)
	}
}

func TestRunWithConfig_PreflightCapabilityProbeFailure_FailsWhenStrict(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLI(t, "gemini", "probe error", 2)
	t.Setenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES", "1")

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-strict", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected strict capability probe failure, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider google capability probe failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected fail in preflight report, got %+v", report.Summary)
	}
}

func TestPreflightReport_IncludesCLIProfileAndSource(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)
	geminiCLI := writeFakeCLI(t, "gemini", "Usage: gemini -p --output-format stream-json --yolo --approval-mode", 0)
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendCLI,
	})
	cfg.LLM.Providers["google"] = ProviderConfig{Backend: BackendCLI, Executable: geminiCLI}
	dot := singleProviderDot("google", "gemini-3-pro-preview")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-report-shape", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	report := mustReadPreflightReport(t, logsRoot)
	if got, want := report.CLIProfile, "test_shim"; got != want {
		t.Fatalf("cli_profile=%q want %q", got, want)
	}
	if !report.AllowTestShim {
		t.Fatalf("allow_test_shim=false want true")
	}
	found := false
	for _, check := range report.Checks {
		if check.Name != "provider_cli_presence" || check.Provider != "google" {
			continue
		}
		found = true
		if got, _ := check.Details["source"].(string); got != "config.executable" {
			t.Fatalf("provider_cli_presence.details.source=%q want %q", got, "config.executable")
		}
	}
	if !found {
		t.Fatalf("missing provider_cli_presence check for google")
	}
}

func singleProviderDot(provider, modelID string) []byte {
	return []byte(fmt.Sprintf(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  a [shape=box, llm_provider="%s", llm_model="%s", prompt="x"]
  exit [shape=Msquare]
  start -> a -> exit
}
`, provider, modelID))
}

func singleProviderModelAttrDot(provider, modelID string) []byte {
	return []byte(fmt.Sprintf(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  a [shape=box, llm_provider="%s", model="%s", prompt="x"]
  exit [shape=Msquare]
  start -> a -> exit
}
`, provider, modelID))
}

func testPreflightConfigForProviders(repo string, catalog string, providers map[string]BackendKind) *RunConfigFile {
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{}
	for provider, backend := range providers {
		cfg.LLM.Providers[provider] = ProviderConfig{Backend: backend}
	}
	cfg.ModelDB.LiteLLMCatalogPath = catalog
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	return cfg
}

func writeCatalogForPreflight(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return p
}

func writeFakeCLI(t *testing.T, name string, helpOutput string, helpExit int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
cat <<'EOF'
%s
EOF
exit %d
fi
echo "ok"
`, helpOutput, helpExit)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake cli: %v", err)
	}
	return p
}

func writeFakeCLIWithModelProbeFailure(t *testing.T, name string, helpOutput string, helpExit int, failModel string, failStderr string, failExit int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]] || [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
cat <<'EOF'
%s
EOF
exit %d
fi
model=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --model)
      model="${2:-}"
      shift 2
      ;;
    *)
      shift
      ;;
  esac
done
if [[ "$model" == %q ]]; then
cat <<'ERR' >&2
%s
ERR
exit %d
fi
echo "ok"
`, helpOutput, helpExit, failModel, failStderr, failExit)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake cli with model probe failure: %v", err)
	}
	return p
}

func mustReadPreflightReport(t *testing.T, logsRoot string) preflightReportDoc {
	t.Helper()
	path := filepath.Join(logsRoot, "preflight_report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read preflight report %s: %v", path, err)
	}
	var report preflightReportDoc
	if err := json.Unmarshal(b, &report); err != nil {
		t.Fatalf("decode preflight report: %v", err)
	}
	return report
}

func writeBlockingProbeCLI(t *testing.T, name string, parentPIDPath string, childPIDPath string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
echo "$$" > %q
sleep 300 &
child="$!"
echo "$child" > %q
wait
`, parentPIDPath, childPIDPath)
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write blocking probe cli: %v", err)
	}
	return p
}
