package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/danshapiro/kilroy/internal/providerspec"

	"gopkg.in/yaml.v3"
)

type BackendKind string

const (
	BackendAPI BackendKind = "api"
	BackendCLI BackendKind = "cli"
)

type ProviderAPIConfig struct {
	Protocol           string            `json:"protocol,omitempty" yaml:"protocol,omitempty"`
	BaseURL            string            `json:"base_url,omitempty" yaml:"base_url,omitempty"`
	Path               string            `json:"path,omitempty" yaml:"path,omitempty"`
	APIKeyEnv          string            `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
	ProviderOptionsKey string            `json:"provider_options_key,omitempty" yaml:"provider_options_key,omitempty"`
	ProfileFamily      string            `json:"profile_family,omitempty" yaml:"profile_family,omitempty"`
	Headers            map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

type ProviderConfig struct {
	Backend    BackendKind       `json:"backend" yaml:"backend"`
	Executable string            `json:"executable,omitempty" yaml:"executable,omitempty"`
	API        ProviderAPIConfig `json:"api,omitempty" yaml:"api,omitempty"`
	Failover   []string          `json:"failover,omitempty" yaml:"failover,omitempty"`
}

type RuntimePolicyConfig struct {
	StageTimeoutMS       *int `json:"stage_timeout_ms,omitempty" yaml:"stage_timeout_ms,omitempty"`
	StallTimeoutMS       *int `json:"stall_timeout_ms,omitempty" yaml:"stall_timeout_ms,omitempty"`
	StallCheckIntervalMS *int `json:"stall_check_interval_ms,omitempty" yaml:"stall_check_interval_ms,omitempty"`
	MaxLLMRetries        *int `json:"max_llm_retries,omitempty" yaml:"max_llm_retries,omitempty"`
}

type PromptProbeConfig struct {
	Enabled     *bool    `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Transports  []string `json:"transports,omitempty" yaml:"transports,omitempty"`
	TimeoutMS   *int     `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
	Retries     *int     `json:"retries,omitempty" yaml:"retries,omitempty"`
	BaseDelayMS *int     `json:"base_delay_ms,omitempty" yaml:"base_delay_ms,omitempty"`
	MaxDelayMS  *int     `json:"max_delay_ms,omitempty" yaml:"max_delay_ms,omitempty"`
}

type PreflightConfig struct {
	PromptProbes PromptProbeConfig `json:"prompt_probes,omitempty" yaml:"prompt_probes,omitempty"`
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
	} `json:"modeldb" yaml:"modeldb"`

	Git struct {
		RequireClean           *bool    `json:"require_clean,omitempty" yaml:"require_clean,omitempty"`
		RunBranchPrefix        string   `json:"run_branch_prefix" yaml:"run_branch_prefix"`
		CommitPerNode          bool     `json:"commit_per_node" yaml:"commit_per_node"`
		PushRemote             string   `json:"push_remote,omitempty" yaml:"push_remote,omitempty"`
		CheckpointExcludeGlobs []string `json:"checkpoint_exclude_globs,omitempty" yaml:"checkpoint_exclude_globs,omitempty"`
	} `json:"git" yaml:"git"`

	Setup struct {
		Commands  []string `json:"commands,omitempty" yaml:"commands,omitempty"`
		TimeoutMS int      `json:"timeout_ms,omitempty" yaml:"timeout_ms,omitempty"`
	} `json:"setup,omitempty" yaml:"setup,omitempty"`

	RuntimePolicy RuntimePolicyConfig `json:"runtime_policy,omitempty" yaml:"runtime_policy,omitempty"`
	Preflight     PreflightConfig     `json:"preflight,omitempty" yaml:"preflight,omitempty"`
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
	// metaspec v1 forces commit_per_node=true; explicit false is ignored.
	if !cfg.Git.CommitPerNode {
		cfg.Git.CommitPerNode = true
	}
	// metaspec default: require_clean defaults to true when not specified.
	if cfg.Git.RequireClean == nil {
		t := true
		cfg.Git.RequireClean = &t
	}
	cfg.Git.CheckpointExcludeGlobs = trimNonEmpty(cfg.Git.CheckpointExcludeGlobs)
	if len(cfg.Git.CheckpointExcludeGlobs) == 0 {
		cfg.Git.CheckpointExcludeGlobs = []string{
			"**/.cargo-target*/**",
			"**/.cargo_target*/**",
			"**/.wasm-pack/**",
			"**/.tmpbuild/**",
		}
	}
	if cfg.LLM.Providers == nil {
		cfg.LLM.Providers = map[string]ProviderConfig{}
	}
	if strings.TrimSpace(cfg.LLM.CLIProfile) == "" {
		cfg.LLM.CLIProfile = "real"
	} else {
		cfg.LLM.CLIProfile = strings.ToLower(strings.TrimSpace(cfg.LLM.CLIProfile))
	}
	cfg.ModelDB.OpenRouterModelInfoPath = strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoPath)
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoUpdatePolicy)
	if cfg.ModelDB.OpenRouterModelInfoUpdatePolicy == "" {
		cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "on_run_start"
	}
	cfg.ModelDB.OpenRouterModelInfoURL = strings.TrimSpace(cfg.ModelDB.OpenRouterModelInfoURL)
	if cfg.ModelDB.OpenRouterModelInfoURL == "" {
		cfg.ModelDB.OpenRouterModelInfoURL = "https://openrouter.ai/api/v1/models"
	}
	if cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS == 0 {
		cfg.ModelDB.OpenRouterModelInfoFetchTimeoutMS = 5000
	}
	if cfg.CXDB.Autostart.WaitTimeoutMS == 0 {
		cfg.CXDB.Autostart.WaitTimeoutMS = 20000
	}
	if cfg.CXDB.Autostart.PollIntervalMS == 0 {
		cfg.CXDB.Autostart.PollIntervalMS = 250
	}
	if cfg.Setup.TimeoutMS == 0 {
		cfg.Setup.TimeoutMS = 300000 // 5 minutes
	}
	cfg.CXDB.Autostart.Command = trimNonEmpty(cfg.CXDB.Autostart.Command)
	cfg.CXDB.Autostart.UI.Command = trimNonEmpty(cfg.CXDB.Autostart.UI.Command)
	cfg.CXDB.Autostart.UI.URL = strings.TrimSpace(cfg.CXDB.Autostart.UI.URL)

	// Runtime policy defaults are explicit to preserve stable operator behavior.
	if cfg.RuntimePolicy.StageTimeoutMS == nil {
		v := 0
		cfg.RuntimePolicy.StageTimeoutMS = &v
	}
	if cfg.RuntimePolicy.StallTimeoutMS == nil {
		v := 600000
		cfg.RuntimePolicy.StallTimeoutMS = &v
	}
	if cfg.RuntimePolicy.StallCheckIntervalMS == nil {
		v := 5000
		cfg.RuntimePolicy.StallCheckIntervalMS = &v
	}
	if cfg.RuntimePolicy.MaxLLMRetries == nil {
		v := 6
		cfg.RuntimePolicy.MaxLLMRetries = &v
	}

	cfg.Preflight.PromptProbes.Transports = trimNonEmpty(cfg.Preflight.PromptProbes.Transports)
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
		return fmt.Errorf("modeldb.openrouter_model_info_path is required")
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
		canonical := providerspec.CanonicalProviderKey(prov)
		builtin, hasBuiltin := providerspec.Builtin(canonical)
		switch pc.Backend {
		case BackendAPI:
			protocol := strings.TrimSpace(pc.API.Protocol)
			if protocol == "" && hasBuiltin && builtin.API != nil {
				protocol = string(builtin.API.Protocol)
			}
			if protocol == "" {
				return fmt.Errorf("llm.providers.%s.api.protocol is required for api backend", prov)
			}
		case BackendCLI:
			if !hasBuiltin || builtin.CLI == nil {
				return fmt.Errorf("llm.providers.%s backend=cli requires builtin provider with cli contract", prov)
			}
		default:
			return fmt.Errorf("invalid backend for provider %q: %q (want api|cli)", prov, pc.Backend)
		}
		if strings.EqualFold(cfg.LLM.CLIProfile, "real") && strings.TrimSpace(pc.Executable) != "" {
			return fmt.Errorf("llm.providers.%s.executable is only allowed when llm.cli_profile=test_shim", prov)
		}
	}
	if cfg.RuntimePolicy.StageTimeoutMS != nil && *cfg.RuntimePolicy.StageTimeoutMS < 0 {
		return fmt.Errorf("runtime_policy.stage_timeout_ms must be >= 0")
	}
	if cfg.RuntimePolicy.StallTimeoutMS != nil && *cfg.RuntimePolicy.StallTimeoutMS < 0 {
		return fmt.Errorf("runtime_policy.stall_timeout_ms must be >= 0")
	}
	if cfg.RuntimePolicy.StallCheckIntervalMS != nil && *cfg.RuntimePolicy.StallCheckIntervalMS < 0 {
		return fmt.Errorf("runtime_policy.stall_check_interval_ms must be >= 0")
	}
	if cfg.RuntimePolicy.MaxLLMRetries != nil && *cfg.RuntimePolicy.MaxLLMRetries < 0 {
		return fmt.Errorf("runtime_policy.max_llm_retries must be >= 0")
	}
	if cfg.RuntimePolicy.StallTimeoutMS != nil && cfg.RuntimePolicy.StallCheckIntervalMS != nil {
		if *cfg.RuntimePolicy.StallTimeoutMS > 0 && *cfg.RuntimePolicy.StallCheckIntervalMS == 0 {
			return fmt.Errorf("runtime_policy.stall_check_interval_ms must be > 0 when stall_timeout_ms > 0")
		}
	}
	if cfg.Preflight.PromptProbes.TimeoutMS != nil && *cfg.Preflight.PromptProbes.TimeoutMS < 0 {
		return fmt.Errorf("preflight.prompt_probes.timeout_ms must be >= 0")
	}
	if cfg.Preflight.PromptProbes.Retries != nil && *cfg.Preflight.PromptProbes.Retries < 0 {
		return fmt.Errorf("preflight.prompt_probes.retries must be >= 0")
	}
	if cfg.Preflight.PromptProbes.BaseDelayMS != nil && *cfg.Preflight.PromptProbes.BaseDelayMS < 0 {
		return fmt.Errorf("preflight.prompt_probes.base_delay_ms must be >= 0")
	}
	if cfg.Preflight.PromptProbes.MaxDelayMS != nil && *cfg.Preflight.PromptProbes.MaxDelayMS < 0 {
		return fmt.Errorf("preflight.prompt_probes.max_delay_ms must be >= 0")
	}
	if cfg.Preflight.PromptProbes.BaseDelayMS != nil && cfg.Preflight.PromptProbes.MaxDelayMS != nil {
		if *cfg.Preflight.PromptProbes.BaseDelayMS > 0 && *cfg.Preflight.PromptProbes.MaxDelayMS > 0 &&
			*cfg.Preflight.PromptProbes.MaxDelayMS < *cfg.Preflight.PromptProbes.BaseDelayMS {
			return fmt.Errorf("preflight.prompt_probes.max_delay_ms must be >= base_delay_ms when both are > 0")
		}
	}
	if len(cfg.Preflight.PromptProbes.Transports) > 0 {
		normalized, err := normalizePromptProbeTransports(cfg.Preflight.PromptProbes.Transports)
		if err != nil {
			return err
		}
		cfg.Preflight.PromptProbes.Transports = normalized
	}
	return nil
}

func normalizeProviderKey(k string) string {
	return providerspec.CanonicalProviderKey(k)
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

// resolveRequireClean returns the effective require_clean value from the config,
// defaulting to true when the config is nil or the field is unset.
func resolveRequireClean(cfg *RunConfigFile) bool {
	if cfg == nil || cfg.Git.RequireClean == nil {
		return true
	}
	return *cfg.Git.RequireClean
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
