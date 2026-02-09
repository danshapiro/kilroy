package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")

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
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
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

func TestRunWithConfig_PreflightPromptProbe_UsesOnlyAPIProvidersInGraph(t *testing.T) {
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"},
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)

	var openaiCalls atomic.Int32
	var sawPrompt atomic.Bool
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		openaiCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if strings.Contains(string(b), preflightPromptProbeText) {
			sawPrompt.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_preflight",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"OK"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k-test")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai":    {Backend: BackendAPI},
		"anthropic": {Backend: BackendAPI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := singleProviderDot("openai", "gpt-5.2")
	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-api-used-only", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}
	if got := openaiCalls.Load(); got == 0 {
		t.Fatalf("expected openai preflight probe request, got %d", got)
	}
	if !sawPrompt.Load() {
		t.Fatalf("expected preflight probe request body to include probe prompt")
	}

	report := mustReadPreflightReport(t, logsRoot)
	foundOpenAIProbe := false
	for _, check := range report.Checks {
		if check.Provider == "anthropic" {
			t.Fatalf("unexpected anthropic preflight check for unused provider: %+v", check)
		}
		if check.Name == "provider_prompt_probe" && check.Provider == "openai" && check.Status == "pass" {
			foundOpenAIProbe = true
		}
	}
	if !foundOpenAIProbe {
		t.Fatalf("missing successful provider_prompt_probe check for openai")
	}
}

func TestRunWithConfig_PreflightPromptProbe_CLIArgMode(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)

	promptSeen := filepath.Join(t.TempDir(), "prompt-arg-seen")
	claudeCLI := filepath.Join(t.TempDir(), "claude")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]]; then
cat <<'EOF'
Usage: claude -p --output-format stream-json --verbose --model MODEL
EOF
exit 0
fi
found=0
for arg in "$@"; do
  if [[ "$arg" == %q ]]; then
    found=1
    break
  fi
done
if [[ "$found" != "1" ]]; then
  echo "missing prompt probe arg" >&2
  exit 7
fi
echo "seen" > %q
echo "ok"
`, preflightPromptProbeText, promptSeen)
	if err := os.WriteFile(claudeCLI, []byte(script), 0o755); err != nil {
		t.Fatalf("write claude fake cli: %v", err)
	}

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"anthropic": BackendCLI,
	})
	cfg.LLM.Providers["anthropic"] = ProviderConfig{Backend: BackendCLI, Executable: claudeCLI}
	dot := singleProviderDot("anthropic", "claude-sonnet-4-20250514")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-cli-arg", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}
	if _, statErr := os.Stat(promptSeen); statErr != nil {
		t.Fatalf("expected cli arg prompt probe marker %s: %v", promptSeen, statErr)
	}
}

func TestRunWithConfig_PreflightPromptProbe_CLIStdinMode(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	promptSeen := filepath.Join(t.TempDir(), "prompt-stdin-seen")
	codexCLI := filepath.Join(t.TempDir(), "codex")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
cat <<'EOF'
Usage: codex exec --json --sandbox workspace-write
EOF
exit 0
fi
prompt="$(cat)"
if [[ "$prompt" != %q ]]; then
  echo "missing prompt probe stdin" >&2
  exit 8
fi
echo "seen" > %q
echo "ok"
`, preflightPromptProbeText, promptSeen)
	if err := os.WriteFile(codexCLI, []byte(script), 0o755); err != nil {
		t.Fatalf("write codex fake cli: %v", err)
	}

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendCLI,
	})
	cfg.LLM.Providers["openai"] = ProviderConfig{Backend: BackendCLI, Executable: codexCLI}
	dot := singleProviderDot("openai", "gpt-5.2")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-cli-stdin", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}
	if _, statErr := os.Stat(promptSeen); statErr != nil {
		t.Fatalf("expected cli stdin prompt probe marker %s: %v", promptSeen, statErr)
	}
}

func TestRunWithConfig_PreflightPromptProbe_AllProvidersWhenGraphUsesAll(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"},
    {"id": "anthropic/claude-sonnet-4-20250514"},
    {"id": "google/gemini-3-pro-preview"},
    {"id": "kimi/kimi-k2.5"},
    {"id": "zai/glm-4.7"}
  ]
}`)

	var openaiCalls atomic.Int32
	var kimiCalls atomic.Int32
	var zaiCalls atomic.Int32
	apiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if !strings.Contains(string(b), preflightPromptProbeText) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"missing preflight prompt probe text"}`))
			return
		}
		switch r.URL.Path {
		case "/v1/responses":
			openaiCalls.Add(1)
		case "/coding/v1/messages":
			kimiCalls.Add(1)
		case "/api/coding/paas/v4/chat/completions":
			zaiCalls.Add(1)
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v1/responses" {
			_, _ = w.Write([]byte(`{
  "id":"resp_preflight",
  "model":"gpt-5.2",
  "output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}],
  "usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}
}`))
			return
		}
		if r.URL.Path == "/coding/v1/messages" {
			_, _ = w.Write([]byte(`{
  "id":"msg_preflight",
  "type":"message",
  "role":"assistant",
  "content":[{"type":"text","text":"OK"}],
  "model":"kimi-for-coding",
  "stop_reason":"end_turn",
  "usage":{"input_tokens":1,"output_tokens":1}
}`))
			return
		}
		_, _ = w.Write([]byte(`{
  "id":"chat_preflight",
  "model":"m",
  "choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"OK"}}],
  "usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}
}`))
	}))
	defer apiSrv.Close()

	claudeCLI := writeFakeCLI(t, "claude", "Usage: claude -p --output-format stream-json --verbose --model MODEL", 0)
	geminiCLI := writeFakeCLI(t, "gemini", "Usage: gemini -p --output-format stream-json --yolo --model MODEL", 0)

	t.Setenv("OPENAI_API_KEY", "k-openai")
	t.Setenv("OPENAI_BASE_URL", apiSrv.URL)
	t.Setenv("KIMI_API_KEY", "k-kimi")
	t.Setenv("ZAI_API_KEY", "k-zai")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
		"anthropic": {
			Backend:    BackendCLI,
			Executable: claudeCLI,
		},
		"google": {
			Backend:    BackendCLI,
			Executable: geminiCLI,
		},
		"kimi": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				BaseURL:       apiSrv.URL + "/coding",
				APIKeyEnv:     "KIMI_API_KEY",
			},
		},
		"zai": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				Protocol:      "openai_chat_completions",
				BaseURL:       apiSrv.URL,
				Path:          "/api/coding/paas/v4/chat/completions",
				APIKeyEnv:     "ZAI_API_KEY",
				ProfileFamily: "openai",
			},
		},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  n1 [shape=box, llm_provider="openai", llm_model="gpt-5.2", prompt="x"]
  n2 [shape=box, llm_provider="anthropic", llm_model="claude-sonnet-4-20250514", prompt="x"]
  n3 [shape=box, llm_provider="google", llm_model="gemini-3-pro-preview", prompt="x"]
  n4 [shape=box, llm_provider="kimi", llm_model="kimi-k2.5", prompt="x"]
  n5 [shape=box, llm_provider="zai", llm_model="glm-4.7", prompt="x"]
  exit [shape=Msquare]
  start -> n1 -> n2 -> n3 -> n4 -> n5 -> exit
}
`)

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "preflight-all-providers",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
	})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}

	if openaiCalls.Load() == 0 {
		t.Fatalf("expected openai preflight prompt probe request")
	}
	if kimiCalls.Load() == 0 {
		t.Fatalf("expected kimi preflight prompt probe request")
	}
	if zaiCalls.Load() == 0 {
		t.Fatalf("expected zai preflight prompt probe request")
	}

	report := mustReadPreflightReport(t, logsRoot)
	wantProviders := map[string]bool{
		"openai":    false,
		"anthropic": false,
		"google":    false,
		"kimi":      false,
		"zai":       false,
	}
	for _, check := range report.Checks {
		if check.Name != "provider_prompt_probe" || check.Status != "pass" {
			continue
		}
		if _, ok := wantProviders[check.Provider]; ok {
			wantProviders[check.Provider] = true
		}
	}
	for provider, seen := range wantProviders {
		if !seen {
			t.Fatalf("missing provider_prompt_probe pass check for %s", provider)
		}
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
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
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
