package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type BackendKind string

const (
	BackendAPI BackendKind = "api"
	BackendCLI BackendKind = "cli"
)

type ProviderConfig struct {
	Backend    BackendKind `json:"backend" yaml:"backend"`
	Executable string      `json:"executable,omitempty" yaml:"executable,omitempty"`
}

type RunConfigFile struct {
	Version int `json:"version" yaml:"version"`

	Repo struct {
		Path string `json:"path" yaml:"path"`
	} `json:"repo" yaml:"repo"`

	CXDB struct {
		BinaryAddr  string `json:"binary_addr" yaml:"binary_addr"`
		HTTPBaseURL string `json:"http_base_url" yaml:"http_base_url"`
		Autostart   struct {
			Enabled        bool     `json:"enabled" yaml:"enabled"`
			Command        []string `json:"command" yaml:"command"`
			WaitTimeoutMS  int      `json:"wait_timeout_ms" yaml:"wait_timeout_ms"`
			PollIntervalMS int      `json:"poll_interval_ms" yaml:"poll_interval_ms"`
			UI             struct {
				Enabled bool     `json:"enabled" yaml:"enabled"`
				Command []string `json:"command" yaml:"command"`
				URL     string   `json:"url" yaml:"url"`
			} `json:"ui" yaml:"ui"`
		} `json:"autostart" yaml:"autostart"`
	} `json:"cxdb" yaml:"cxdb"`

	LLM struct {
		CLIProfile string                    `json:"cli_profile" yaml:"cli_profile"`
		Providers  map[string]ProviderConfig `json:"providers" yaml:"providers"`
	} `json:"llm" yaml:"llm"`

	ModelDB struct {
		OpenRouterModelInfoPath           string `json:"openrouter_model_info_path" yaml:"openrouter_model_info_path"`
		OpenRouterModelInfoUpdatePolicy   string `json:"openrouter_model_info_update_policy" yaml:"openrouter_model_info_update_policy"`
		OpenRouterModelInfoURL            string `json:"openrouter_model_info_url" yaml:"openrouter_model_info_url"`
		OpenRouterModelInfoFetchTimeoutMS int    `json:"openrouter_model_info_fetch_timeout_ms" yaml:"openrouter_model_info_fetch_timeout_ms"`

		// Deprecated compatibility aliases.
		LiteLLMCatalogPath           string `json:"litellm_catalog_path" yaml:"litellm_catalog_path"`
		LiteLLMCatalogUpdatePolicy   string `json:"litellm_catalog_update_policy" yaml:"litellm_catalog_update_policy"`
		LiteLLMCatalogURL            string `json:"litellm_catalog_url" yaml:"litellm_catalog_url"`
		LiteLLMCatalogFetchTimeoutMS int    `json:"litellm_catalog_fetch_timeout_ms" yaml:"litellm_catalog_fetch_timeout_ms"`
	} `json:"modeldb" yaml:"modeldb"`

	Git struct {
		RequireClean    bool   `json:"require_clean" yaml:"require_clean"`
		RunBranchPrefix string `json:"run_branch_prefix" yaml:"run_branch_prefix"`
		CommitPerNode   bool   `json:"commit_per_node" yaml:"commit_per_node"`
	} `json:"git" yaml:"git"`
}

func LoadRunConfigFile(path string) (*RunConfigFile, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg RunConfigFile
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".json":
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, err
		}
	default:
		if err := yaml.Unmarshal(b, &cfg); err != nil {
			return nil, err
		}
	}
	applyConfigDefaults(&cfg)
	if err := validateConfig(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func applyConfigDefaults(cfg *RunConfigFile) {
	if cfg == nil {
		return
	}
	if cfg.Version == 0 {
		cfg.Version = 1
	}
	if cfg.Git.RunBranchPrefix == "" {
		cfg.Git.RunBranchPrefix = "attractor/run"
	}
	// metaspec default.
	if !cfg.Git.CommitPerNode {
		cfg.Git.CommitPerNode = true
	}
	// metaspec default.
	if !cfg.Git.RequireClean {
		cfg.Git.RequireClean = true
	}
	if cfg.LLM.Providers == nil {
		cfg.LLM.Providers = map[string]ProviderConfig{}
	}
	if strings.TrimSpace(cfg.LLM.CLIProfile) == "" {
		cfg.LLM.CLIProfile = "real"
	} else {
		cfg.LLM.CLIProfile = strings.ToLower(strings.TrimSpace(cfg.LLM.CLIProfile))
	}
	cfg.ModelDB.OpenRouterModelInfoPath = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.OpenRouterModelInfoPath,
		cfg.ModelDB.LiteLLMCatalogPath,
	))
	cfg.ModelDB.LiteLLMCatalogPath = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.LiteLLMCatalogPath,
		cfg.ModelDB.OpenRouterModelInfoPath,
	))
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.OpenRouterModelInfoUpdatePolicy,
		cfg.ModelDB.LiteLLMCatalogUpdatePolicy,
	))
	if cfg.ModelDB.OpenRouterModelInfoUpdatePolicy == "" {
		cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "on_run_start"
	}
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.LiteLLMCatalogUpdatePolicy,
		cfg.ModelDB.OpenRouterModelInfoUpdatePolicy,
	))
	cfg.ModelDB.OpenRouterModelInfoURL = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.OpenRouterModelInfoURL,
		cfg.ModelDB.LiteLLMCatalogURL,
	))
	if cfg.ModelDB.OpenRouterModelInfoURL == "" {
		cfg.ModelDB.OpenRouterModelInfoURL = "https://openrouter.ai/api/v1/models"
	}
	cfg.ModelDB.LiteLLMCatalogURL = strings.TrimSpace(firstNonEmpty(
		cfg.ModelDB.LiteLLMCatalogURL,
		cfg.ModelDB.OpenRouterModelInfoURL,
	))
	if cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS == 0 {
		cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS = cfg.ModelDB.LiteLLMCatalogFetchTimeoutMS
	}
	if cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS == 0 {
		cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS = 5000
	}
	if cfg.ModelDB.LiteLLMCatalogFetchTimeoutMS == 0 {
		cfg.ModelDB.LiteLLMCatalogFetchTimeoutMS = cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS
	}
	if cfg.CXDB.Autostart.WaitTimeoutMS == 0 {
		cfg.CXDB.Autostart.WaitTimeoutMS = 20000
	}
	if cfg.CXDB.Autostart.PollIntervalMS == 0 {
		cfg.CXDB.Autostart.PollIntervalMS = 250
	}
	cfg.CXDB.Autostart.Command = trimNonEmpty(cfg.CXDB.Autostart.Command)
	cfg.CXDB.Autostart.UI.Command = trimNonEmpty(cfg.CXDB.Autostart.UI.Command)
	cfg.CXDB.Autostart.UI.URL = strings.TrimSpace(cfg.CXDB.Autostart.UI.URL)
}

func validateConfig(cfg *RunConfigFile) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if cfg.Version != 1 {
		return fmt.Errorf("unsupported config version: %d", cfg.Version)
	}
	if strings.TrimSpace(cfg.Repo.Path) == "" {
		return fmt.Errorf("repo.path is required")
	}
	if strings.TrimSpace(cfg.CXDB.BinaryAddr) == "" || strings.TrimSpace(cfg.CXDB.HTTPBaseURL) == "" {
		return fmt.Errorf("cxdb.binary_addr and cxdb.http_base_url are required in v1")
	}
	if cfg.CXDB.Autostart.WaitTimeoutMS < 0 {
		return fmt.Errorf("cxdb.autostart.wait_timeout_ms must be >= 0")
	}
	if cfg.CXDB.Autostart.PollIntervalMS < 0 {
		return fmt.Errorf("cxdb.autostart.poll_interval_ms must be >= 0")
	}
	if cfg.CXDB.Autostart.Enabled && len(cfg.CXDB.Autostart.Command) == 0 {
		return fmt.Errorf("cxdb.autostart.command is required when cxdb.autostart.enabled=true")
	}
	if strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoPath) == "" {
		return fmt.Errorf("modeldb.openrouter_model_info_path is required (deprecated alias: modeldb.litellm_catalog_path)")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoUpdatePolicy)) {
	case "pinned", "on_run_start":
		// ok
	default:
		return fmt.Errorf("invalid modeldb.openrouter_model_info_update_policy: %q (want pinned|on_run_start)", cfg.ModelDB.OpenRouterModelInfoUpdatePolicy)
	}
	if strings.ToLower(strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoUpdatePolicy)) == "on_run_start" && strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoURL) == "" {
		return fmt.Errorf("modeldb.openrouter_model_info_url is required when update_policy=on_run_start")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.LLM.CLIProfile)) {
	case "real", "test_shim":
		// ok
	default:
		return fmt.Errorf("invalid llm.cli_profile: %q (want real|test_shim)", cfg.LLM.CLIProfile)
	}
	for prov, pc := range cfg.LLM.Providers {
		switch normalizeProviderKey(prov) {
		case "openai", "anthropic", "google":
			// ok
		default:
			return fmt.Errorf("unsupported provider in config: %q", prov)
		}
		switch pc.Backend {
		case BackendAPI, BackendCLI:
		default:
			return fmt.Errorf("invalid backend for provider %q: %q (want api|cli)", prov, pc.Backend)
		}
		if strings.EqualFold(cfg.LLM.CLIProfile, "real") && strings.TrimSpace(pc.Executable) != "" {
			return fmt.Errorf("llm.providers.%s.executable is only allowed when llm.cli_profile=test_shim", prov)
		}
	}
	return nil
}

func normalizeProviderKey(k string) string {
	k = strings.ToLower(strings.TrimSpace(k))
	switch k {
	case "gemini":
		return "google"
	default:
		return k
	}
}

func trimNonEmpty(parts []string) []string {
	if len(parts) == 0 {
		return nil
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
