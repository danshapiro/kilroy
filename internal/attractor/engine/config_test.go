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
  litellm_catalog_path: /tmp/catalog.json
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
  "modeldb": {"litellm_catalog_path": "/tmp/catalog.json"}
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

func TestNormalizeProviderKey_GeminiMapsToGoogle(t *testing.T) {
	if got := normalizeProviderKey("gemini"); got != "google" {
		t.Fatalf("normalizeProviderKey(gemini)=%q want google", got)
	}
	if got := normalizeProviderKey("GOOGLE"); got != "google" {
		t.Fatalf("normalizeProviderKey(GOOGLE)=%q want google", got)
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
  litellm_catalog_path: /tmp/catalog.json
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
  litellm_catalog_path: /tmp/catalog.json
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
  litellm_catalog_path: /tmp/catalog.json
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
  litellm_catalog_path: /tmp/catalog.json
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
  litellm_catalog_path: /tmp/catalog.json
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
  litellm_catalog_path: /tmp/catalog.json
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
