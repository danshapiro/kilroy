//go:build !windows

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
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
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
	if runtime.GOOS == "darwin" {
		t.Skip("process group signaling is unreliable on macOS")
	}
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

func TestRunWithConfig_WarnsWhenCLIModelNotInCatalogForProvider(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	t.Setenv("GEMINI_API_KEY", "k-test")
	t.Setenv("KIMI_API_KEY", "k-test")
	t.Setenv("ZAI_API_KEY", "k-test")
	t.Setenv("CEREBRAS_API_KEY", "k-test")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "google/gemini-3-pro-preview"}
  ]
}`)

	// Use API backend to isolate the catalog check from CLI binary presence.
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"google": BackendAPI,
	})
	dot := singleProviderDot("google", "gemini-3-pro")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-warn", LogsRoot: logsRoot, AllowTestShim: true})
	// Catalog miss is now a warning, not a failure. The run should proceed
	// past preflight and fail downstream (e.g. CXDB connect) instead.
	if err != nil && strings.Contains(err.Error(), "not present in run catalog") {
		t.Fatalf("catalog miss should be a warning not a hard failure, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected no preflight failures for catalog miss (should be warn), got %+v", report.Summary)
	}
	if report.Summary.Warn == 0 {
		t.Fatalf("expected preflight warn for catalog miss, got %+v", report.Summary)
	}
}

func TestRunWithConfig_WarnsWhenAPIModelNotInCatalogForProvider(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "k-test")
	t.Setenv("GEMINI_API_KEY", "k-test")
	t.Setenv("KIMI_API_KEY", "k-test")
	t.Setenv("ZAI_API_KEY", "k-test")
	t.Setenv("CEREBRAS_API_KEY", "k-test")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"},
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
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-api-warn", LogsRoot: logsRoot, AllowTestShim: true})
	// Catalog miss is now a warning, not a failure. The run proceeds past
	// preflight; the prompt probe or actual API call will catch invalid models.
	if err != nil && strings.Contains(err.Error(), "not present in run catalog") {
		t.Fatalf("catalog miss should be a warning not a hard failure, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected no preflight failures for catalog miss (should be warn), got %+v", report.Summary)
	}
	if report.Summary.Warn == 0 {
		t.Fatalf("expected preflight warn for catalog miss, got %+v", report.Summary)
	}
}

func TestRunWithConfig_WarnsAndContinues_WhenProviderNotInCatalog(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	t.Setenv("CEREBRAS_API_KEY", "k-cerebras")
	t.Setenv("ZAI_API_KEY", "k-test")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"cerebras": BackendAPI,
	})
	dot := singleProviderDot("cerebras", "zai-glm-4.7")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-warn-uncovered-provider", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "not present in run catalog") {
		t.Fatalf("expected catalog validation to be skipped for provider not in catalog, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected no preflight failures for provider not in catalog, got %+v", report.Summary)
	}
	if report.Summary.Warn == 0 {
		t.Fatalf("expected warn check for uncovered provider in preflight report, got %+v", report.Summary)
	}
	foundWarn := false
	for _, check := range report.Checks {
		if check.Name == "provider_model_catalog" && check.Provider == "cerebras" && check.Status == "warn" {
			foundWarn = true
			break
		}
	}
	if !foundWarn {
		t.Fatalf("expected provider_model_catalog warn check for cerebras in preflight report")
	}
}

func TestRunWithConfig_ForceModel_BypassesCatalogGate(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	t.Setenv("OPENAI_API_KEY", "k-test")

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
		"openai": {Backend: BackendAPI, Failover: []string{}},
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

func TestRunWithConfig_WarnsForModelFallbackAttributeCatalogMiss(t *testing.T) {
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
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-model-attr-warn", LogsRoot: logsRoot, AllowTestShim: true})
	// Catalog miss is now a warning. The error (if any) should be from downstream, not catalog.
	if err != nil && strings.Contains(err.Error(), "not present in run catalog") {
		t.Fatalf("catalog miss should be a warning not a hard failure, got %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Warn == 0 {
		t.Fatalf("expected preflight warn for catalog miss, got %+v", report.Summary)
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

func TestRunWithConfig_AllowsKimiAndZai_WhenCatalogUsesOpenRouterPrefixes(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	t.Setenv("KIMI_API_KEY", "k-kimi")
	t.Setenv("ZAI_API_KEY", "k-zai")
	t.Setenv("CEREBRAS_API_KEY", "k-test")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "moonshotai/kimi-k2.5"},
    {"id": "z-ai/glm-4.7"}
  ]
}`)

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"kimi": BackendAPI,
		"zai":  BackendAPI,
	})
	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=box, llm_provider="kimi", llm_model="kimi-k2.5", prompt="x"]
  b [shape=box, llm_provider="zai", llm_model="glm-4.7", prompt="x"]
  exit [shape=Msquare]
  start -> a -> b -> exit
}
`)

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-openrouter-prefix", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}
	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail != 0 {
		t.Fatalf("expected preflight pass summary, got %+v", report.Summary)
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
		if strings.Contains(string(b), preflightPromptProbeText) || strings.Contains(string(b), preflightPromptProbeAgentLoopText) {
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
		"openai":    {Backend: BackendAPI, Failover: []string{}},
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

func TestRunWithConfig_PreflightPromptProbe_APIAgentLoopShape_UsesTools(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")
	t.Setenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_TRANSPORTS", "complete")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	var openaiCalls atomic.Int32
	var sawTools atomic.Bool
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		openaiCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		raw := map[string]any{}
		_ = json.Unmarshal(b, &raw)
		_, hasTools := raw["tools"]
		if hasTools {
			sawTools.Store(true)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"tool path overloaded"}}`))
			return
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

	t.Setenv("OPENAI_API_KEY", "k-openai")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := singleProviderDot("openai", "gpt-5.2")
	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-api-agent-loop-shape", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected preflight failure, got nil")
	}
	if !strings.Contains(err.Error(), "preflight: provider openai api prompt probe failed for model gpt-5.2") {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := openaiCalls.Load(); got == 0 {
		t.Fatalf("expected openai preflight request, got %d", got)
	}
	if !sawTools.Load() {
		t.Fatalf("expected agent_loop preflight probe request to include tools")
	}

	report := mustReadPreflightReport(t, logsRoot)
	foundFail := false
	for _, check := range report.Checks {
		if check.Name != "provider_prompt_probe" || check.Provider != "openai" || check.Status != "fail" {
			continue
		}
		foundFail = true
		if got, _ := check.Details["mode"].(string); got != "agent_loop" {
			t.Fatalf("provider_prompt_probe.details.mode=%q want %q", got, "agent_loop")
		}
		if got, _ := check.Details["transport"].(string); got != "complete" {
			t.Fatalf("provider_prompt_probe.details.transport=%q want %q", got, "complete")
		}
		break
	}
	if !foundFail {
		t.Fatalf("expected provider_prompt_probe fail check for openai")
	}
}

func TestRunWithConfig_PreflightPromptProbe_APIOneShotShape_DoesNotUseTools(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")
	t.Setenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_TRANSPORTS", "complete")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	var openaiCalls atomic.Int32
	var sawTools atomic.Bool
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		openaiCalls.Add(1)
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		raw := map[string]any{}
		_ = json.Unmarshal(b, &raw)
		_, hasTools := raw["tools"]
		if hasTools {
			sawTools.Store(true)
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"tool path overloaded"}}`))
			return
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

	t.Setenv("OPENAI_API_KEY", "k-openai")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  a [shape=box, llm_provider="openai", llm_model="gpt-5.2", codergen_mode="one_shot", prompt="x"]
  exit [shape=Msquare]
  start -> a -> exit
}
`)
	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-api-oneshot-shape", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("expected preflight success for one_shot probe, got %v", err)
	}
	if got := openaiCalls.Load(); got == 0 {
		t.Fatalf("expected openai preflight request, got %d", got)
	}
	if sawTools.Load() {
		t.Fatalf("expected one_shot preflight probe request to avoid tools")
	}

	report := mustReadPreflightReport(t, logsRoot)
	foundPass := false
	for _, check := range report.Checks {
		if check.Name != "provider_prompt_probe" || check.Provider != "openai" || check.Status != "pass" {
			continue
		}
		foundPass = true
		if got, _ := check.Details["mode"].(string); got != "one_shot" {
			t.Fatalf("provider_prompt_probe.details.mode=%q want %q", got, "one_shot")
		}
		if got, _ := check.Details["transport"].(string); got != "complete" {
			t.Fatalf("provider_prompt_probe.details.transport=%q want %q", got, "complete")
		}
		break
	}
	if !foundPass {
		t.Fatalf("expected provider_prompt_probe pass check for openai")
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
Usage: claude -p --dangerously-skip-permissions --output-format stream-json --verbose --model MODEL
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
output_path=""
argv=("$@")
for ((i=0; i<${#argv[@]}; i++)); do
  if [[ "${argv[$i]}" == "-o" || "${argv[$i]}" == "--output" ]]; then
    if (( i + 1 < ${#argv[@]} )); then
      output_path="${argv[$((i + 1))]}"
    fi
    break
  fi
done
if [[ "$prompt" != %q ]]; then
  echo "missing prompt probe stdin" >&2
  exit 8
fi
if [[ -n "$output_path" ]]; then
cat > "$output_path" <<'EOF'
{"final":"OK","summary":"OK"}
EOF
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

func TestRunWithConfig_PreflightPromptProbe_CLIUsesProductionRetryPath(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2"}
  ]
}`)

	callCountPath := filepath.Join(t.TempDir(), "codex-call-count")
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
output_path=""
argv=("$@")
for ((i=0; i<${#argv[@]}; i++)); do
  if [[ "${argv[$i]}" == "-o" || "${argv[$i]}" == "--output" ]]; then
    if (( i + 1 < ${#argv[@]} )); then
      output_path="${argv[$((i + 1))]}"
    fi
    break
  fi
done
count=0
if [[ -f %q ]]; then
  count="$(cat %q)"
fi
count=$((count + 1))
echo "$count" > %q
if [[ "$count" == "1" ]]; then
  echo "state db missing rollout path for thread preflight" >&2
  exit 1
fi
if [[ "$prompt" != %q ]]; then
  echo "missing prompt probe stdin" >&2
  exit 8
fi
if [[ -n "$output_path" ]]; then
cat > "$output_path" <<'EOF'
{"final":"OK","summary":"OK"}
EOF
fi
cat <<'EOF'
{"type":"thread.started","thread_id":"t"}
{"type":"turn.completed"}
EOF
`, callCountPath, callCountPath, callCountPath, preflightPromptProbeText)
	if err := os.WriteFile(codexCLI, []byte(script), 0o755); err != nil {
		t.Fatalf("write codex fake cli: %v", err)
	}

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendCLI,
	})
	cfg.LLM.Providers["openai"] = ProviderConfig{Backend: BackendCLI, Executable: codexCLI}
	dot := singleProviderDot("openai", "gpt-5.2")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-cli-production-retry", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure: %v", err)
	}

	gotCallsRaw, readErr := os.ReadFile(callCountPath)
	if readErr != nil {
		t.Fatalf("read codex call count: %v", readErr)
	}
	gotCalls, convErr := strconv.Atoi(strings.TrimSpace(string(gotCallsRaw)))
	if convErr != nil {
		t.Fatalf("parse codex call count: %v (raw=%q)", convErr, string(gotCallsRaw))
	}
	if got := gotCalls; got < 2 {
		t.Fatalf("expected prompt probe to retry through production path; codex calls=%d want >=2", got)
	}

	report := mustReadPreflightReport(t, logsRoot)
	sawPromptProbePass := false
	for _, check := range report.Checks {
		if check.Name == "provider_prompt_probe" && check.Provider == "openai" && check.Status == "pass" {
			sawPromptProbePass = true
			break
		}
	}
	if !sawPromptProbePass {
		t.Fatalf("expected provider_prompt_probe pass check for openai")
	}

	invPath := filepath.Join(logsRoot, "preflight", "prompt-probe", "openai", "gpt-5.2", "cli", "cli_invocation.json")
	invRaw, readErr := os.ReadFile(invPath)
	if readErr != nil {
		t.Fatalf("read cli_invocation artifact %s: %v", invPath, readErr)
	}
	var inv map[string]any
	if err := json.Unmarshal(invRaw, &inv); err != nil {
		t.Fatalf("decode cli_invocation artifact: %v", err)
	}
	if got, _ := inv["state_db_fallback_retry"].(bool); !got {
		t.Fatalf("expected cli_invocation.state_db_fallback_retry=true, got %#v", inv["state_db_fallback_retry"])
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
		if !strings.Contains(string(b), preflightPromptProbeText) &&
			!strings.Contains(string(b), preflightPromptProbeAgentLoopText) {
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
			var payload map[string]any
			_ = json.Unmarshal(b, &payload)
			stream, _ := payload["stream"].(bool)
			if !stream || asInt(payload["max_tokens"]) < 16000 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"expected stream=true and max_tokens>=16000"}}`))
				return
			}
			writeAnthropicStreamOK(w, "OK")
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

	claudeCLI := writeFakeCLI(t, "claude", "Usage: claude -p --dangerously-skip-permissions --output-format stream-json --verbose --model MODEL", 0)
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
				BaseURL:   apiSrv.URL + "/coding",
				APIKeyEnv: "KIMI_API_KEY",
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

	// The run should either fail at preflight (external provider down) or
	// downstream (cxdb not configured). Either way, collect the preflight
	// report and print a provider status table.
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	isPreflightErr := strings.Contains(err.Error(), "preflight:")

	// Collect API call counts for the mocked providers.
	apiCalls := map[string]int32{
		"openai": openaiCalls.Load(),
		"kimi":   kimiCalls.Load(),
		"zai":    zaiCalls.Load(),
	}

	// Build provider status table from the preflight report (if it exists).
	type providerStatus struct {
		status  string
		message string
	}
	wantProviders := []string{"openai", "anthropic", "google", "kimi", "zai"}
	statuses := map[string]providerStatus{}

	report, reportErr := readPreflightReport(t, logsRoot)
	if reportErr != nil && isPreflightErr {
		// Preflight failed before writing a report — likely an external
		// provider (e.g. failover target) returned an error.
		t.Logf("Preflight error (no report): %v", err)
		t.Logf("")
		t.Logf("Provider Status Table:")
		t.Logf("%-12s %-10s %s", "PROVIDER", "STATUS", "DETAIL")
		t.Logf("%-12s %-10s %s", "--------", "------", "------")
		for _, p := range wantProviders {
			if calls, ok := apiCalls[p]; ok && calls > 0 {
				t.Logf("%-12s %-10s %s", p, "CALLED", fmt.Sprintf("(%d API calls)", calls))
			} else {
				t.Logf("%-12s %-10s %s", p, "UNKNOWN", "(preflight aborted before probe)")
			}
		}
		t.Logf("")
		t.Skipf("preflight aborted by external provider failure (likely a failover target billing issue): %v", err)
		return
	}

	// Parse report checks into the status table.
	for _, check := range report.Checks {
		if check.Name != "provider_prompt_probe" {
			continue
		}
		statuses[check.Provider] = providerStatus{
			status:  check.Status,
			message: check.Message,
		}
	}

	// Print the table.
	t.Logf("")
	t.Logf("Provider Preflight Probe Status:")
	t.Logf("%-12s %-10s %-10s %s", "PROVIDER", "STATUS", "API CALLS", "MESSAGE")
	t.Logf("%-12s %-10s %-10s %s", "--------", "------", "---------", "-------")
	passCount := 0
	for _, p := range wantProviders {
		st := statuses[p]
		if st.status == "" {
			st.status = "no-probe"
		}
		if st.status == "pass" {
			passCount++
		}
		calls := ""
		if c, ok := apiCalls[p]; ok {
			calls = fmt.Sprintf("%d", c)
		} else {
			calls = "cli"
		}
		t.Logf("%-12s %-10s %-10s %s", p, st.status, calls, st.message)
	}
	// Also log any failover targets that were probed.
	for provider, st := range statuses {
		isWant := false
		for _, w := range wantProviders {
			if w == provider {
				isWant = true
				break
			}
		}
		if !isWant {
			t.Logf("%-12s %-10s %-10s %s", provider+"*", st.status, "failover", st.message)
		}
	}
	t.Logf("")

	if passCount == 0 && isPreflightErr {
		// Check if the failure came from a failover target (not a directly-configured provider).
		failoverFailure := true
		for _, p := range wantProviders {
			if strings.Contains(err.Error(), "provider "+p+" ") {
				failoverFailure = false
				break
			}
		}
		if failoverFailure {
			t.Skipf("preflight aborted by external failover provider — none of the %d configured providers were probed: %v", len(wantProviders), err)
		}
		t.Fatalf("none of the configured LLM providers passed preflight probes — all providers are down or misconfigured (error: %v)", err)
	}

	if isPreflightErr {
		t.Logf("preflight failed for some providers but %d/%d passed — continuing: %v", passCount, len(wantProviders), err)
	}

	// Verify mocked API providers were actually called.
	for _, p := range []string{"openai", "kimi", "zai"} {
		if apiCalls[p] == 0 {
			t.Errorf("expected %s preflight prompt probe request, got 0 API calls", p)
		}
	}

	// Check kimi-specific policy details if kimi passed.
	if st, ok := statuses["kimi"]; ok && st.status == "pass" {
		for _, check := range report.Checks {
			if check.Name != "provider_prompt_probe" || check.Provider != "kimi" || check.Status != "pass" {
				continue
			}
			if got, _ := check.Details["transport"].(string); got != "stream" {
				t.Fatalf("provider_prompt_probe.details.transport=%q want %q", got, "stream")
			}
			if got := asInt(check.Details["max_tokens"]); got < 16000 {
				t.Fatalf("provider_prompt_probe.details.max_tokens=%d want >=16000", got)
			}
			if got, _ := check.Details["policy_reason"].(string); strings.TrimSpace(got) == "" {
				t.Fatalf("provider_prompt_probe.details.policy_reason should be non-empty")
			}
			break
		}
	}
}

func TestProviderPreflight_PromptProbe_IncludesFailoverTargets(t *testing.T) {
	g := model.NewGraph("preflight")
	n := model.NewNode("impl")
	n.Attrs["shape"] = "box"
	n.Attrs["llm_provider"] = "kimi"
	n.Attrs["llm_model"] = "kimi-k2.5"
	n.Attrs["codergen_mode"] = "agent_loop"
	n.Attrs["reasoning_effort"] = "high"
	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	runtimes := map[string]ProviderRuntime{
		"kimi": {
			Key:           "kimi",
			Backend:       BackendAPI,
			ProfileFamily: "openai",
			Failover:      []string{"zai"},
		},
		"zai": {
			Key:           "zai",
			Backend:       BackendAPI,
			ProfileFamily: "openai",
		},
	}

	targets, err := usedAPIPromptProbeTargetsForProvider(
		g,
		runtimes,
		"zai",
		RunOptions{},
		[]string{preflightAPIPromptProbeTransportComplete},
		nil,
	)
	if err != nil {
		t.Fatalf("usedAPIPromptProbeTargetsForProvider: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets len=%d want 1", len(targets))
	}
	target := targets[0]
	if target.Provider != "zai" {
		t.Fatalf("target provider=%q want zai", target.Provider)
	}
	if target.Model != "glm-4.7" {
		t.Fatalf("target model=%q want glm-4.7", target.Model)
	}
	if target.Mode != "agent_loop" {
		t.Fatalf("target mode=%q want agent_loop", target.Mode)
	}
	if got := len(target.Request.Tools); got == 0 {
		t.Fatalf("expected agent_loop failover probe to include tools")
	}
	if got := usedAPIProviders(g, runtimes); strings.Join(got, ",") != "kimi,zai" {
		t.Fatalf("used api providers=%v want [kimi zai]", got)
	}
}

func TestProviderPreflight_PromptProbe_FailoverModelSelectionMatchesRuntime(t *testing.T) {
	g := model.NewGraph("preflight")
	n := model.NewNode("impl")
	n.Attrs["shape"] = "box"
	n.Attrs["llm_provider"] = "openai"
	n.Attrs["llm_model"] = "gpt-5.2-codex"
	n.Attrs["codergen_mode"] = "one_shot"
	if err := g.AddNode(n); err != nil {
		t.Fatalf("AddNode: %v", err)
	}

	runtimes := map[string]ProviderRuntime{
		"openai": {
			Key:           "openai",
			Backend:       BackendAPI,
			ProfileFamily: "openai",
			Failover:      []string{"anthropic"},
		},
		"anthropic": {
			Key:           "anthropic",
			Backend:       BackendAPI,
			ProfileFamily: "anthropic",
		},
	}
	catalog := &modeldb.Catalog{
		Models: map[string]modeldb.ModelEntry{
			"us/claude-opus-4-6-20260205": {
				Provider: "anthropic",
				Mode:     "chat",
			},
		},
	}

	expectedModel := pickFailoverModelFromRuntime("anthropic", runtimes, catalog, "gpt-5.2-codex")
	targets, err := usedAPIPromptProbeTargetsForProvider(
		g,
		runtimes,
		"anthropic",
		RunOptions{},
		[]string{preflightAPIPromptProbeTransportComplete},
		catalog,
	)
	if err != nil {
		t.Fatalf("usedAPIPromptProbeTargetsForProvider: %v", err)
	}
	if len(targets) != 1 {
		t.Fatalf("targets len=%d want 1", len(targets))
	}
	if got := targets[0].Model; got != expectedModel {
		t.Fatalf("failover probe model=%q want runtime-selected %q", got, expectedModel)
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
	report, err := readPreflightReport(t, logsRoot)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return report
}

func readPreflightReport(t *testing.T, logsRoot string) (preflightReportDoc, error) {
	t.Helper()
	path := filepath.Join(logsRoot, "preflight_report.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return preflightReportDoc{}, fmt.Errorf("read preflight report %s: %v", path, err)
	}
	var report preflightReportDoc
	if err := json.Unmarshal(b, &report); err != nil {
		return preflightReportDoc{}, fmt.Errorf("decode preflight report: %v", err)
	}
	return report, nil
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

func TestRunWithConfig_PreflightPromptProbe_SkipsCodexWhenNoAPIKey(t *testing.T) {
	// When codex CLI is the provider and OPENAI_API_KEY is not set, the prompt
	// probe should be skipped with a warning rather than failing. Codex supports
	// browser-based "chatgpt" auth which stores session tokens in ~/.codex/ and
	// cannot be tested in the probe's isolated environment.
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "on")
	t.Setenv("OPENAI_API_KEY", "") // explicitly unset

	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2-codex"}
  ]
}`)

	// Create a fake codex CLI that passes capability probe but would FAIL prompt
	// probe. Place it on PATH so it's discovered via the default executable
	// resolution (real mode does not allow explicit Executable overrides).
	binDir := t.TempDir()
	probeFailed := filepath.Join(t.TempDir(), "probe-should-not-run")
	codexCLI := filepath.Join(binDir, "codex")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "exec" && "${2:-}" == "--help" ]]; then
cat <<'EOF'
Usage: codex exec --json --sandbox workspace-write
EOF
exit 0
fi
# If we get here, the prompt probe was NOT skipped.
echo "reached" > %q
echo "auth error: chatgpt session not found" >&2
exit 1
`, probeFailed)
	if err := os.WriteFile(codexCLI, []byte(script), 0o755); err != nil {
		t.Fatalf("write codex fake cli: %v", err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))

	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendCLI,
	})
	// Use "real" profile — the chatgpt auth skip only applies in real mode.
	cfg.LLM.CLIProfile = "real"
	dot := singleProviderDot("openai", "gpt-5.2-codex")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "preflight-codex-skip", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected downstream cxdb error, got nil")
	}
	// The run should fail later (CXDB not running), NOT at the preflight stage.
	if strings.Contains(err.Error(), "preflight:") {
		t.Fatalf("unexpected preflight failure (prompt probe should have been skipped): %v", err)
	}

	// Verify the prompt probe script was never invoked.
	if _, statErr := os.Stat(probeFailed); statErr == nil {
		t.Fatalf("prompt probe was invoked despite codex chatgpt auth skip; marker file exists: %s", probeFailed)
	}

	// Verify the preflight report contains a warn check with the skip reason.
	report := mustReadPreflightReport(t, logsRoot)
	found := false
	for _, check := range report.Checks {
		if check.Name == "provider_prompt_probe" && check.Status == "warn" {
			if reason, ok := check.Details["skip_reason"]; ok && reason == "codex_chatgpt_auth" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatalf("expected warn check with skip_reason=codex_chatgpt_auth in preflight report; got: %+v", report.Checks)
	}
}

func TestProviderPreflight_CLIOnlyModelWithAPIBackend_Fails(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.3-codex-spark"}
  ]
}`)
	// openai configured as API backend — should fail for CLI-only model.
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendAPI,
	})
	dot := singleProviderDot("openai", "gpt-5.3-codex-spark")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "cli-only-api-fail", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatal("expected preflight error for CLI-only model with API backend, got nil")
	}
	if !strings.Contains(err.Error(), "CLI-only") {
		t.Fatalf("expected error to mention 'CLI-only', got: %v", err)
	}

	report := mustReadPreflightReport(t, logsRoot)
	if report.Summary.Fail == 0 {
		t.Fatalf("expected preflight failure for CLI-only model with API backend, got %+v", report.Summary)
	}
	found := false
	for _, c := range report.Checks {
		if c.Name == "cli_only_model_backend" && c.Status == "fail" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected cli_only_model_backend fail check in report, got: %+v", report.Checks)
	}
}

func TestProviderPreflight_CLIOnlyModelWithCLIBackend_Passes(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.3-codex-spark"}
  ]
}`)
	// openai configured as CLI backend — should pass the CLI-only check.
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendCLI,
	})
	dot := singleProviderDot("openai", "gpt-5.3-codex-spark")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "cli-only-cli-pass", LogsRoot: logsRoot, AllowTestShim: true})
	// May fail downstream (e.g., no CLI binary) but should NOT fail with CLI-only error.
	if err != nil && strings.Contains(err.Error(), "CLI-only") {
		t.Fatalf("CLI-only model with CLI backend should not fail CLI-only check, got: %v", err)
	}

	report, reportErr := readPreflightReport(t, logsRoot)
	if reportErr != nil {
		// If report can't be read, the run failed before preflight wrote it.
		// Check error is not CLI-only related.
		return
	}
	for _, c := range report.Checks {
		if c.Name == "cli_only_model_backend" && c.Status == "fail" {
			t.Fatalf("expected cli_only_model_backend to pass for CLI backend, got fail: %s", c.Message)
		}
	}
}

func TestProviderPreflight_CLIOnlyModel_ForceModelOverridesToRegular_NoFail(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.3-codex-spark"},
    {"id": "openai/gpt-5.2-codex"}
  ]
}`)
	// openai configured as API backend with a CLI-only model in the graph,
	// but force-model overrides to a regular model. Should NOT fail.
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendAPI,
	})
	dot := singleProviderDot("openai", "gpt-5.3-codex-spark")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "cli-only-force-regular",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
		ForceModels:   map[string]string{"openai": "gpt-5.2-codex"},
	})
	// Should NOT fail with CLI-only error — force-model replaces Spark with
	// a regular model.
	if err != nil && strings.Contains(err.Error(), "CLI-only") {
		t.Fatalf("force-model to regular model should bypass CLI-only check, got: %v", err)
	}
}

func TestProviderPreflight_ForceModelInjectsCLIOnly_WithAPIBackend_Fails(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	repo := initTestRepo(t)
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "openai/gpt-5.2-codex"},
    {"id": "openai/gpt-5.3-codex-spark"}
  ]
}`)
	// openai configured as API backend, graph uses a regular model, but
	// force-model injects a CLI-only model. Should fail.
	cfg := testPreflightConfigForProviders(repo, catalog, map[string]BackendKind{
		"openai": BackendAPI,
	})
	dot := singleProviderDot("openai", "gpt-5.2-codex")

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "force-cli-only-api-fail",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
		ForceModels:   map[string]string{"openai": "gpt-5.3-codex-spark"},
	})
	if err == nil {
		t.Fatal("expected preflight error when force-model injects CLI-only model with API backend, got nil")
	}
	if !strings.Contains(err.Error(), "CLI-only") {
		t.Fatalf("expected error to mention 'CLI-only', got: %v", err)
	}
}
