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
)

func TestRunWithConfig_FailsFastWhenProviderBackendMissing(t *testing.T) {
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="hi"]
  start -> a -> exit
}
`)
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = "/tmp/repo"
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"
	// Intentionally omit llm.providers.openai.backend

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "r1", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "hint:") {
		t.Errorf("error should contain a hint for the user, got: %s", err.Error())
	}
}

func TestRunWithConfig_ReportsCXDBUIURL(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.CXDB.Autostart.UI.URL = "http://127.0.0.1:9020"
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "ui-url", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if got, want := res.CXDBUIURL, "http://127.0.0.1:9020"; got != want {
		t.Fatalf("res.CXDBUIURL=%q want %q", got, want)
	}
}

func TestRunWithConfig_RejectsTestShimWithoutAllowFlag(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.LLM.CLIProfile = "test_shim"
	err := validateRunCLIProfilePolicy(cfg, RunOptions{}, true)
	if err == nil {
		t.Fatalf("expected test_shim gate error, got nil")
	}
	if !strings.Contains(err.Error(), "llm.cli_profile=test_shim requires --allow-test-shim") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithConfig_DoesNotRequireAllowTestShim_ForAPIOnlyProviders(t *testing.T) {
	repo := initTestRepo(t)
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider="openai", llm_model="gpt-5.4", prompt="hi"]
  start -> a -> exit
}
`)
	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "api-no-shim-gate", LogsRoot: logsRoot})
	// The test_shim gate should NOT apply to API-only providers.
	if err != nil && strings.Contains(err.Error(), "--allow-test-shim") {
		t.Fatalf("did not expect test_shim gate for api-only run: %v", err)
	}
	// Catalog miss is now a warn (not hard fail), so it should not be the error.
	if err != nil && strings.Contains(err.Error(), "not present in run catalog") {
		t.Fatalf("catalog miss should be a warning not a hard failure, got %v", err)
	}
}

func TestRunWithConfig_RejectsRealProfileWhenProviderPathEnvIsSet(t *testing.T) {
	repo := initTestRepo(t)
	t.Setenv("KILROY_CODEX_PATH", "/tmp/fake/codex")

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "real-env-reject", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected real profile env override error, got nil")
	}
	if !strings.Contains(err.Error(), "llm.cli_profile=real forbids provider path overrides") {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "KILROY_CODEX_PATH") {
		t.Fatalf("expected env key in error, got %v", err)
	}
}

func TestRunWithConfig_ProfilePolicyFailure_WritesPreflightReport(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	t.Setenv("KILROY_CODEX_PATH", "/tmp/fake/codex")

	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.LLM.CLIProfile = "real"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="hi"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "real-env-report", LogsRoot: logsRoot})
	if err == nil {
		t.Fatalf("expected policy error, got nil")
	}

	reportPath := filepath.Join(logsRoot, "preflight_report.json")
	b, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("read %s: %v", reportPath, readErr)
	}
	var report struct {
		Summary struct {
			Fail int `json:"fail"`
		} `json:"summary"`
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if unmarshalErr := json.Unmarshal(b, &report); unmarshalErr != nil {
		t.Fatalf("decode preflight report: %v", unmarshalErr)
	}
	if report.Summary.Fail == 0 {
		t.Fatalf("expected fail count in preflight report, got %+v", report.Summary)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "provider_executable_policy" && check.Status == "fail" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected provider_executable_policy fail check, got %+v", report.Checks)
	}
}

func TestPreflightWithConfig_SkipsRunExecutionArtifacts(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	runID := "preflight-skip-exec"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := PreflightWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       runID,
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("PreflightWithConfig: %v", err)
	}
	if res == nil {
		t.Fatal("PreflightWithConfig returned nil result")
	}

	reportPath := filepath.Join(logsRoot, "preflight_report.json")
	if got, want := res.PreflightReportPath, reportPath; got != want {
		t.Fatalf("PreflightReportPath: got %q want %q", got, want)
	}
	assertExists(t, reportPath)

	for _, rel := range []string{"final.json", "checkpoint.json", "manifest.json", "run.pid"} {
		p := filepath.Join(logsRoot, rel)
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("expected %s to be absent; stat err=%v", p, statErr)
		}
	}
	worktreeDir := filepath.Join(logsRoot, "worktree")
	if _, statErr := os.Stat(worktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("expected worktree dir to be absent; stat err=%v", statErr)
	}

	runBranch := "attractor/run/" + runID
	if got := strings.TrimSpace(runCmdOut(t, repo, "git", "branch", "--list", runBranch)); got != "" {
		t.Fatalf("expected run branch %q to be absent, got %q", runBranch, got)
	}
}

func TestPreflightWithConfig_ReturnsRunAndReportMetadata(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	runID := "preflight-metadata"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := PreflightWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       runID,
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("PreflightWithConfig: %v", err)
	}
	if res == nil {
		t.Fatal("PreflightWithConfig returned nil result")
	}
	if got, want := res.RunID, runID; got != want {
		t.Fatalf("RunID: got %q want %q", got, want)
	}
	if got, want := res.LogsRoot, logsRoot; got != want {
		t.Fatalf("LogsRoot: got %q want %q", got, want)
	}

	reportPath := filepath.Join(logsRoot, "preflight_report.json")
	if got, want := res.PreflightReportPath, reportPath; got != want {
		t.Fatalf("PreflightReportPath: got %q want %q", got, want)
	}
	b, readErr := os.ReadFile(reportPath)
	if readErr != nil {
		t.Fatalf("read preflight_report.json: %v", readErr)
	}
	var decoded map[string]any
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode preflight_report.json: %v", err)
	}
}

func TestPreflightWithConfig_StillEnforcesRunPolicyGates(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="hi"]
  start -> a -> exit
}
`)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := PreflightWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "preflight-gate",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err == nil {
		t.Fatal("expected test_shim gate error, got nil")
	}
	if !strings.Contains(err.Error(), "llm.cli_profile=test_shim requires --allow-test-shim") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunWithConfig_WritesPIDFile(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "pid-file", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	pidPath := filepath.Join(res.LogsRoot, "run.pid")
	b, readErr := os.ReadFile(pidPath)
	if readErr != nil {
		t.Fatalf("expected run.pid to exist after foreground run, got: %v", readErr)
	}
	pidStr := strings.TrimSpace(string(b))
	if pidStr == "" {
		t.Fatal("run.pid is empty")
	}
	pid, parseErr := strconv.Atoi(pidStr)
	if parseErr != nil || pid <= 0 {
		t.Fatalf("run.pid contains invalid pid: %q", pidStr)
	}
}

func writeProviderCatalogForTest(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(p, []byte(`{
  "data": [
    {
      "id": "kimi/kimi-k2.5",
      "context_length": 32768,
      "supported_parameters": ["tools"],
      "architecture": {"input_modalities": ["text"], "output_modalities": ["text"]},
      "pricing": {"prompt": "0.000001", "completion": "0.000002"},
      "top_provider": {"context_length": 32768, "max_completion_tokens": 8192}
    },
    {
      "id": "zai/glm-4.7",
      "context_length": 131072,
      "supported_parameters": ["tools"],
      "architecture": {"input_modalities": ["text"], "output_modalities": ["text"]},
      "pricing": {"prompt": "0.000001", "completion": "0.000002"},
      "top_provider": {"context_length": 131072, "max_completion_tokens": 8192}
    },
    {
      "id": "minimax/minimax-m2.5",
      "context_length": 196608,
      "supported_parameters": ["tools"],
      "architecture": {"input_modalities": ["text"], "output_modalities": ["text"]},
      "pricing": {"prompt": "0.00000015", "completion": "0.0000012"},
      "top_provider": {"context_length": 196608, "max_completion_tokens": 16384}
    }
  ]
}`), 0o644); err != nil {
		t.Fatalf("write catalog: %v", err)
	}
	return p
}

func TestRunWithConfig_AcceptsKimiAndZaiAPIProviders(t *testing.T) {
	repo := initTestRepo(t)
	cxdbSrv := newCXDBTestServer(t)
	catalogPath := writeProviderCatalogForTest(t)

	cases := []struct {
		provider string
		model    string
		protocol string
		keyEnv   string
		baseURL  string
		path     string
	}{
		{
			provider: "kimi",
			model:    "kimi-k2.5",
			protocol: "anthropic_messages",
			keyEnv:   "KIMI_API_KEY",
			baseURL:  "http://127.0.0.1:1/coding",
		},
		{
			provider: "zai",
			model:    "glm-4.7",
			protocol: "openai_chat_completions",
			keyEnv:   "ZAI_API_KEY",
			baseURL:  "http://127.0.0.1:1",
			path:     "/api/coding/paas/v4/chat/completions",
		},
		{
			provider: "minimax",
			model:    "minimax-m2.5",
			protocol: "openai_chat_completions",
			keyEnv:   "MINIMAX_API_KEY",
			baseURL:  "http://127.0.0.1:1",
			path:     "/v1/chat/completions",
		},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			dot := []byte(fmt.Sprintf(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=%s, llm_model=%s, prompt="hi"]
  start -> a -> exit
}
	`, tc.provider, tc.model))
			cfg := &RunConfigFile{Version: 1}
			cfg.Repo.Path = repo
			cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
			cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
			cfg.ModelDB.OpenRouterModelInfoPath = catalogPath
			cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
			zeroRetries := 0
			cfg.RuntimePolicy.MaxLLMRetries = &zeroRetries
			cfg.LLM.Providers = map[string]ProviderConfig{
				tc.provider: {
					Backend:  BackendAPI,
					Failover: []string{},
					API: ProviderAPIConfig{
						Protocol:      tc.protocol,
						APIKeyEnv:     tc.keyEnv,
						BaseURL:       tc.baseURL,
						Path:          tc.path,
						ProfileFamily: "openai",
					},
				},
			}
			for _, envKey := range []string{
				"OPENAI_API_KEY",
				"ANTHROPIC_API_KEY",
				"GEMINI_API_KEY",
				"KIMI_API_KEY",
				"ZAI_API_KEY",
				"CEREBRAS_API_KEY",
				"MINIMAX_API_KEY",
			} {
				t.Setenv(envKey, "")
			}
			t.Setenv(tc.keyEnv, "k-test")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "r1-" + tc.provider, LogsRoot: t.TempDir()})
			if err != nil {
				if strings.Contains(err.Error(), "unsupported provider") {
					t.Fatalf("provider should be accepted, got %v", err)
				}
				if strings.Contains(err.Error(), "not found in model catalog") {
					t.Fatalf("provider/model should pass catalog validation, got %v", err)
				}
			}
		})
	}
}
