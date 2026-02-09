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

func TestRunWithConfig_FailsFastWhenProviderBackendMissing(t *testing.T) {
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
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
	repo := initTestRepo(t)
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: "/tmp/fake/codex"},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = writePinnedCatalog(t)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "shim-no-allow", LogsRoot: t.TempDir()})
	if err == nil {
		t.Fatalf("expected test_shim gate error, got nil")
	}
	if !strings.Contains(err.Error(), "llm.cli_profile=test_shim requires --allow-test-shim") {
		t.Fatalf("unexpected error: %v", err)
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
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
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
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="hi"]
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
			cfg.LLM.Providers = map[string]ProviderConfig{
				tc.provider: {
					Backend: BackendAPI,
					API: ProviderAPIConfig{
						Protocol:      tc.protocol,
						APIKeyEnv:     tc.keyEnv,
						BaseURL:       tc.baseURL,
						Path:          tc.path,
						ProfileFamily: "openai",
					},
				},
			}
			t.Setenv(tc.keyEnv, "k-test")
			_, err := RunWithConfig(context.Background(), dot, cfg, RunOptions{RunID: "r1-" + tc.provider, LogsRoot: t.TempDir()})
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
