package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRunConfigFile_YAMLAndJSON(t *testing.T) {
	dir := t.TempDir()

	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  providers:
    openai:
      backend: api
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRunConfigFile(yml)
	if err != nil {
		t.Fatalf("LoadRunConfigFile(yaml): %v", err)
	}
	if cfg.Version != 1 || strings.TrimSpace(cfg.Repo.Path) == "" {
		t.Fatalf("cfg: %+v", cfg)
	}
	if cfg.LLM.Providers["openai"].Backend != BackendAPI {
		t.Fatalf("openai backend: %q", cfg.LLM.Providers["openai"].Backend)
	}

	js := filepath.Join(dir, "run.json")
	if err := os.WriteFile(js, []byte(`{
  "version": 1,
  "repo": {"path": "/tmp/repo"},
  "cxdb": {"binary_addr": "127.0.0.1:9009", "http_base_url": "http://127.0.0.1:9010"},
  "llm": {"providers": {"anthropic": {"backend": "cli"}}},
  "modeldb": {"openrouter_model_info_path": "/tmp/catalog.json"}
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadRunConfigFile(js)
	if err != nil {
		t.Fatalf("LoadRunConfigFile(json): %v", err)
	}
	if cfg2.LLM.Providers["anthropic"].Backend != BackendCLI {
		t.Fatalf("anthropic backend: %q", cfg2.LLM.Providers["anthropic"].Backend)
	}
}

func TestLoadRunConfigFile_ModelDBOpenRouterKeys(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  providers:
    openai:
      backend: api
modeldb:
  openrouter_model_info_path: /tmp/openrouter.json
  openrouter_model_info_update_policy: pinned
  openrouter_model_info_url: https://openrouter.ai/api/v1/models
  openrouter_model_info_fetch_timeout_ms: 3456
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRunConfigFile(yml)
	if err != nil {
		t.Fatalf("LoadRunConfigFile(yaml): %v", err)
	}
	if got, want := cfg.ModelDB.OpenRouterModelInfoPath, "/tmp/openrouter.json"; got != want {
		t.Fatalf("openrouter_model_info_path=%q want %q", got, want)
	}
	if got, want := cfg.ModelDB.OpenRouterModelInfoUpdatePolicy, "pinned"; got != want {
		t.Fatalf("openrouter_model_info_update_policy=%q want %q", got, want)
	}
	if got, want := cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS, 3456; got != want {
		t.Fatalf("openrouter_model_info_fetch_timeout_ms=%d want %d", got, want)
	}
}

func TestNormalizeProviderKey_GeminiMapsToGoogle(t *testing.T) {
	if got := normalizeProviderKey("gemini"); got != "google" {
		t.Fatalf("normalizeProviderKey(gemini)=%q want google", got)
	}
	if got := normalizeProviderKey("GOOGLE"); got != "google" {
		t.Fatalf("normalizeProviderKey(GOOGLE)=%q want google", got)
	}
}

func TestNormalizeProviderKey_DelegatesToProviderSpecAliases(t *testing.T) {
	if got := normalizeProviderKey("z-ai"); got != "zai" {
		t.Fatalf("normalizeProviderKey(z-ai)=%q want zai", got)
	}
	if got := normalizeProviderKey("moonshot"); got != "kimi" {
		t.Fatalf("normalizeProviderKey(moonshot)=%q want kimi", got)
	}
}

func TestLoadRunConfigFile_CXDBAutostartDefaultsAndTrim(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
  autostart:
    enabled: true
    command: ["  sh  ", "", "  -c", " echo ok "]
llm:
  providers:
    openai:
      backend: api
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRunConfigFile(yml)
	if err != nil {
		t.Fatalf("LoadRunConfigFile(yaml): %v", err)
	}
	if !cfg.CXDB.Autostart.Enabled {
		t.Fatalf("expected autostart enabled")
	}
	if got, want := cfg.CXDB.Autostart.WaitTimeoutMS, 20000; got != want {
		t.Fatalf("wait_timeout_ms=%d want %d", got, want)
	}
	if got, want := cfg.CXDB.Autostart.PollIntervalMS, 250; got != want {
		t.Fatalf("poll_interval_ms=%d want %d", got, want)
	}
	if got, want := strings.Join(cfg.CXDB.Autostart.Command, " "), "sh -c echo ok"; got != want {
		t.Fatalf("autostart command=%q want %q", got, want)
	}
}

func TestLoadRunConfigFile_CXDBAutostartValidation(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
  autostart:
    enabled: true
llm:
  providers:
    openai:
      backend: api
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRunConfigFile(yml); err == nil || !strings.Contains(err.Error(), "cxdb.autostart.command") {
		t.Fatalf("expected autostart command validation error, got: %v", err)
	}
}

func TestLoadRunConfigFile_CXDBAutostartUIAllowsAutodiscovery(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
  autostart:
    ui:
      enabled: true
llm:
  providers:
    openai:
      backend: api
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRunConfigFile(yml)
	if err != nil {
		t.Fatalf("expected config to load for UI autodiscovery defaults, got: %v", err)
	}
	if !cfg.CXDB.Autostart.UI.Enabled {
		t.Fatalf("expected ui.enabled=true")
	}
}

func TestLoadRunConfigFile_DefaultCLIProfileIsReal(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  providers:
    openai:
      backend: cli
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadRunConfigFile(yml)
	if err != nil {
		t.Fatalf("LoadRunConfigFile: %v", err)
	}
	if got := cfg.LLM.CLIProfile; got != "real" {
		t.Fatalf("cli_profile=%q want real", got)
	}
}

func loadRunConfigFromBytesForTest(t *testing.T, yml []byte) (*RunConfigFile, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "run.yaml")
	if err := os.WriteFile(p, yml, 0o644); err != nil {
		t.Fatalf("write run.yaml: %v", err)
	}
	return LoadRunConfigFile(p)
}

func TestLoadRunConfig_CustomAPIProviderRequiresProtocol(t *testing.T) {
	yml := []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
llm:
  providers:
    acme:
      backend: api
`)
	_, err := loadRunConfigFromBytesForTest(t, yml)
	if err == nil || !strings.Contains(err.Error(), "llm.providers.acme.api.protocol") {
		t.Fatalf("expected protocol validation error, got %v", err)
	}
}

func TestLoadRunConfig_KimiAPIProtocolAccepted(t *testing.T) {
	yml := []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
llm:
  providers:
    kimi:
      backend: api
      api:
        protocol: anthropic_messages
        api_key_env: KIMI_API_KEY
        base_url: https://api.kimi.com/coding
        path: /v1/messages
`)
	cfg, err := loadRunConfigFromBytesForTest(t, yml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := cfg.LLM.Providers["kimi"].API.Protocol; got != "anthropic_messages" {
		t.Fatalf("protocol not parsed: %q", got)
	}
}

func TestLoadRunConfig_ZAIAliasAcceptedWithAPIProtocol(t *testing.T) {
	yml := []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
llm:
  providers:
    z-ai:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: ZAI_API_KEY
`)
	if _, err := loadRunConfigFromBytesForTest(t, yml); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRunConfig_BackwardCompatibleBuiltinProvidersStillValid(t *testing.T) {
	yml := []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
modeldb: { openrouter_model_info_path: /tmp/catalog.json }
llm:
  providers:
    openai: { backend: api }
    anthropic: { backend: api }
    google: { backend: api }
`)
	if _, err := loadRunConfigFromBytesForTest(t, yml); err != nil {
		t.Fatalf("unexpected backward-compat validation error: %v", err)
	}
}

func TestBackwardCompatibility_OpenAIAnthropicGoogleStillValid(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = "/tmp/repo"
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai":    {Backend: BackendAPI},
		"anthropic": {Backend: BackendAPI},
		"google":    {Backend: BackendAPI},
	}
	applyConfigDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}

func TestLoadRunConfigFile_InvalidCLIProfile(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  cli_profile: banana
  providers:
    openai:
      backend: cli
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRunConfigFile(yml)
	if err == nil {
		t.Fatalf("expected invalid cli_profile error")
	}
	if !strings.Contains(err.Error(), "invalid llm.cli_profile") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadRunConfigFile_ExecutableOverrideRequiresTestShim(t *testing.T) {
	dir := t.TempDir()
	yml := filepath.Join(dir, "run.yaml")
	if err := os.WriteFile(yml, []byte(`
version: 1
repo:
  path: /tmp/repo
cxdb:
  binary_addr: 127.0.0.1:9009
  http_base_url: http://127.0.0.1:9010
llm:
  cli_profile: real
  providers:
    openai:
      backend: cli
      executable: /tmp/fake/codex
modeldb:
  openrouter_model_info_path: /tmp/catalog.json
`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadRunConfigFile(yml)
	if err == nil {
		t.Fatalf("expected executable override validation error")
	}
	if !strings.Contains(err.Error(), "llm.providers.openai.executable") || !strings.Contains(err.Error(), "test_shim") {
		t.Fatalf("unexpected error: %v", err)
	}
}
