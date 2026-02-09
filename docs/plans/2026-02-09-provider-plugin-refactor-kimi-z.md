# Provider Plug-in Refactor + Kimi/Z API Support Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace hard-coded provider branching with a provider plug-in architecture so Kimi and Z are supported via API immediately and new providers can be added with config + protocol selection rather than engine code edits.

**Architecture:** Add a provider-spec registry (built-in defaults plus run-config overrides), refactor API/CLI routing to consume runtime provider definitions, and select adapters by API protocol family instead of provider name. Keep backward compatibility for `openai`, `anthropic`, and `google`, while adding built-in `kimi` and `zai` API providers. Move agent profile/failover/CLI contracts to data-driven metadata.

**Tech Stack:** Go, YAML (`gopkg.in/yaml.v3`), JSON, `net/http`, existing Kilroy engine/LLM packages, `go test`.

---

### Task 1: Create Provider Spec Registry Core (Single Canonicalization Source)

**Files:**
- Create: `internal/providerspec/spec.go`
- Create: `internal/providerspec/builtin.go`
- Test: `internal/providerspec/spec_test.go`
- Modify: `internal/attractor/engine/config.go`
- Modify: `internal/llm/client.go`
- Test: `internal/attractor/engine/config_test.go`
- Test: `internal/llm/client_test.go`

**Step 1: Write the failing test**

```go
package providerspec

import "testing"

func TestBuiltinSpecsIncludeCoreAndNewProviders(t *testing.T) {
	s := Builtins()
	for _, key := range []string{"openai", "anthropic", "google", "kimi", "zai"} {
		if _, ok := s[key]; !ok {
			t.Fatalf("missing builtin provider %q", key)
		}
	}
}

func TestCanonicalProviderKey_Aliases(t *testing.T) {
	if got := CanonicalProviderKey("gemini"); got != "google" {
		t.Fatalf("gemini alias: got %q want %q", got, "google")
	}
	if got := CanonicalProviderKey(" Z-AI "); got != "zai" {
		t.Fatalf("z-ai alias: got %q want %q", got, "zai")
	}
	if got := CanonicalProviderKey("moonshot"); got != "kimi" {
		t.Fatalf("moonshot alias: got %q want %q", got, "kimi")
	}
	if got := CanonicalProviderKey("glm"); got != "glm" {
		t.Fatalf("glm should remain model-family text, got %q", got)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/providerspec -run 'TestBuiltinSpecsIncludeCoreAndNewProviders|TestCanonicalProviderKey_Aliases' -v`
Expected: FAIL (`internal/providerspec` package does not exist)

**Step 3: Write minimal implementation**

```go
package providerspec

import "strings"

type APIProtocol string

const (
	ProtocolOpenAIResponses      APIProtocol = "openai_responses"
	ProtocolOpenAIChatCompletions APIProtocol = "openai_chat_completions"
	ProtocolAnthropicMessages    APIProtocol = "anthropic_messages"
	ProtocolGoogleGenerateContent APIProtocol = "google_generate_content"
)

type APISpec struct {
	Protocol           APIProtocol
	DefaultBaseURL     string
	DefaultPath        string
	DefaultAPIKeyEnv   string
	ProviderOptionsKey string
	ProfileFamily      string
}

type CLISpec struct {
	DefaultExecutable string
	InvocationTemplate []string
	PromptMode        string
	HelpProbeArgs     []string
	CapabilityAll     []string
	CapabilityAnyOf   [][]string
}

type Spec struct {
	Key      string
	Aliases  []string
	API      *APISpec
	CLI      *CLISpec
	Failover []string
}

var providerAliases = map[string]string{
	"gemini":   "google",
	"z-ai":     "zai",
	"z.ai":     "zai",
	"moonshot": "kimi",
}

func CanonicalProviderKey(in string) string {
	k := strings.ToLower(strings.TrimSpace(in))
	if v, ok := providerAliases[k]; ok {
		return v
	}
	return k
}

func CanonicalizeProviderList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		k := CanonicalProviderKey(raw)
		if k == "" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
```

```go
package providerspec

var builtinSpecs = map[string]Spec{
		"openai": {
			Key:     "openai",
			Aliases: []string{"openai"},
			API: &APISpec{Protocol: ProtocolOpenAIResponses, DefaultBaseURL: "https://api.openai.com", DefaultPath: "/v1/responses", DefaultAPIKeyEnv: "OPENAI_API_KEY", ProviderOptionsKey: "openai", ProfileFamily: "openai"},
			CLI: &CLISpec{DefaultExecutable: "codex", InvocationTemplate: []string{"exec", "--json", "--sandbox", "workspace-write", "-m", "{{model}}", "-C", "{{worktree}}"}, PromptMode: "stdin", HelpProbeArgs: []string{"exec", "--help"}, CapabilityAll: []string{"--json", "--sandbox"}},
			Failover: []string{"anthropic", "google"},
		},
		"anthropic": {
			Key:     "anthropic",
			Aliases: []string{"anthropic"},
			API: &APISpec{Protocol: ProtocolAnthropicMessages, DefaultBaseURL: "https://api.anthropic.com", DefaultPath: "/v1/messages", DefaultAPIKeyEnv: "ANTHROPIC_API_KEY", ProviderOptionsKey: "anthropic", ProfileFamily: "anthropic"},
			CLI: &CLISpec{DefaultExecutable: "claude", InvocationTemplate: []string{"-p", "--output-format", "stream-json", "--verbose", "--model", "{{model}}", "{{prompt}}"}, PromptMode: "arg", HelpProbeArgs: []string{"--help"}, CapabilityAll: []string{"--output-format", "stream-json", "--verbose"}},
			Failover: []string{"openai", "google"},
		},
		"google": {
			Key:     "google",
			Aliases: []string{"google", "gemini"},
			API: &APISpec{Protocol: ProtocolGoogleGenerateContent, DefaultBaseURL: "https://generativelanguage.googleapis.com", DefaultPath: "/v1beta/models/{model}:generateContent", DefaultAPIKeyEnv: "GEMINI_API_KEY", ProviderOptionsKey: "google", ProfileFamily: "google"},
			CLI: &CLISpec{DefaultExecutable: "gemini", InvocationTemplate: []string{"-p", "--output-format", "stream-json", "--yolo", "--model", "{{model}}", "{{prompt}}"}, PromptMode: "arg", HelpProbeArgs: []string{"--help"}, CapabilityAll: []string{"--output-format"}, CapabilityAnyOf: [][]string{{"--yolo", "--approval-mode"}}},
			Failover: []string{"openai", "anthropic"},
		},
		"kimi": {
			Key:     "kimi",
			Aliases: []string{"kimi", "moonshot"},
			API: &APISpec{Protocol: ProtocolOpenAIChatCompletions, DefaultBaseURL: "https://api.moonshot.ai", DefaultPath: "/v1/chat/completions", DefaultAPIKeyEnv: "KIMI_API_KEY", ProviderOptionsKey: "kimi", ProfileFamily: "openai"},
			Failover: []string{"openai", "zai"},
		},
		"zai": {
			Key:     "zai",
			Aliases: []string{"zai", "z-ai", "z.ai"},
			API: &APISpec{Protocol: ProtocolOpenAIChatCompletions, DefaultBaseURL: "https://api.z.ai", DefaultPath: "/api/paas/v4/chat/completions", DefaultAPIKeyEnv: "ZAI_API_KEY", ProviderOptionsKey: "zai", ProfileFamily: "openai"},
			Failover: []string{"openai", "kimi"},
		},
}

func Builtin(key string) (Spec, bool) {
	s, ok := builtinSpecs[CanonicalProviderKey(key)]
	if !ok {
		return Spec{}, false
	}
	return cloneSpec(s), true
}

func Builtins() map[string]Spec {
	out := make(map[string]Spec, len(builtinSpecs))
	for k, v := range builtinSpecs {
		out[k] = cloneSpec(v)
	}
	return out
}

func cloneSpec(in Spec) Spec {
	out := in
	if in.API != nil {
		api := *in.API
		out.API = &api
	}
	if in.CLI != nil {
		cli := *in.CLI
		cli.InvocationTemplate = append([]string{}, in.CLI.InvocationTemplate...)
		cli.HelpProbeArgs = append([]string{}, in.CLI.HelpProbeArgs...)
		cli.CapabilityAll = append([]string{}, in.CLI.CapabilityAll...)
		if len(in.CLI.CapabilityAnyOf) > 0 {
			cli.CapabilityAnyOf = make([][]string, 0, len(in.CLI.CapabilityAnyOf))
			for _, group := range in.CLI.CapabilityAnyOf {
				cli.CapabilityAnyOf = append(cli.CapabilityAnyOf, append([]string{}, group...))
			}
		}
		out.CLI = &cli
	}
	out.Aliases = append([]string{}, in.Aliases...)
	out.Failover = append([]string{}, in.Failover...)
	return out
}
```

```go
// internal/attractor/engine/config.go
func normalizeProviderKey(k string) string {
	return providerspec.CanonicalProviderKey(k)
}
```

```go
// internal/llm/client.go
func normalizeProviderName(name string) string {
	return providerspec.CanonicalProviderKey(name)
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/providerspec -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/providerspec/spec.go internal/providerspec/builtin.go internal/providerspec/spec_test.go internal/attractor/engine/config.go internal/attractor/engine/config_test.go internal/llm/client.go internal/llm/client_test.go
git commit -m "feat(providerspec): add canonical provider registry and unify alias normalization across engine and llm client"
```

### Task 2: Extend Run Config Schema for Provider Plug-ins

**Files:**
- Modify: `internal/attractor/engine/config.go`
- Test: `internal/attractor/engine/config_test.go`

**Step 1: Write the failing test**

```go
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
modeldb: { litellm_catalog_path: /tmp/catalog.json }
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
modeldb: { litellm_catalog_path: /tmp/catalog.json }
llm:
  providers:
    kimi:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: KIMI_API_KEY
        base_url: https://api.moonshot.ai
        path: /v1/chat/completions
`)
	cfg, err := loadRunConfigFromBytesForTest(t, yml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.LLM.Providers["kimi"].API.Protocol != "openai_chat_completions" {
		t.Fatalf("protocol not parsed")
	}
}

func TestLoadRunConfig_ZAIAliasAcceptedWithAPIProtocol(t *testing.T) {
	yml := []byte(`
version: 1
repo: { path: /tmp/repo }
cxdb: { binary_addr: 127.0.0.1:9009, http_base_url: http://127.0.0.1:9010 }
modeldb: { litellm_catalog_path: /tmp/catalog.json }
llm:
  providers:
    z-ai:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: ZAI_API_KEY
`)
	_, err := loadRunConfigFromBytesForTest(t, yml)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run 'TestLoadRunConfig_CustomAPIProviderRequiresProtocol|TestLoadRunConfig_KimiAPIProtocolAccepted|TestLoadRunConfig_ZAIAliasAcceptedWithAPIProtocol' -v`
Expected: FAIL (new `api` fields missing from schema/validation)

**Step 3: Write minimal implementation**

```go
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
	Backend    BackendKind        `json:"backend" yaml:"backend"`
	Executable string             `json:"executable,omitempty" yaml:"executable,omitempty"`
	API        ProviderAPIConfig  `json:"api,omitempty" yaml:"api,omitempty"`
	Failover   []string           `json:"failover,omitempty" yaml:"failover,omitempty"`
}
```

```go
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
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run 'TestLoadRunConfig_CustomAPIProviderRequiresProtocol|TestLoadRunConfig_KimiAPIProtocolAccepted|TestLoadRunConfig_ZAIAliasAcceptedWithAPIProtocol' -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/config.go internal/attractor/engine/config_test.go
git commit -m "feat(config): add provider api schema fields and validation for protocol-driven providers"
```

### Task 3: Build Runtime Provider Definitions (Merged Defaults + Overrides)

**Files:**
- Create: `internal/attractor/engine/provider_runtime.go`
- Test: `internal/attractor/engine/provider_runtime_test.go`

**Step 1: Write the failing test**

```go
func TestResolveProviderRuntimes_MergesBuiltinAndConfigOverrides(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {Backend: BackendAPI, API: ProviderAPIConfig{Protocol: "openai_chat_completions", APIKeyEnv: "KIMI_API_KEY", Headers: map[string]string{"X-Trace": "1"}}},
		"openai": {Backend: BackendAPI},
	}
	rt, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	if rt["kimi"].API.Protocol != "openai_chat_completions" {
		t.Fatalf("kimi protocol mismatch")
	}
	if rt["openai"].API.DefaultPath != "/v1/responses" {
		t.Fatalf("expected openai default path")
	}
	if got := rt["kimi"].APIHeaders(); got["X-Trace"] != "1" {
		t.Fatalf("expected runtime headers copy, got %v", got)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestResolveProviderRuntimes_MergesBuiltinAndConfigOverrides -v`
Expected: FAIL (`resolveProviderRuntimes` undefined)

**Step 3: Write minimal implementation**

```go
type ProviderRuntime struct {
	Key           string
	Backend       BackendKind
	Executable    string
	API           providerspec.APISpec
	CLI           *providerspec.CLISpec
	apiHeaders    map[string]string
	Failover      []string
	ProfileFamily string
}

func (r ProviderRuntime) APIHeaders() map[string]string {
	return cloneStringMap(r.apiHeaders)
}

func resolveProviderRuntimes(cfg *RunConfigFile) (map[string]ProviderRuntime, error) {
	out := map[string]ProviderRuntime{}
	for rawKey, pc := range cfg.LLM.Providers {
		key := providerspec.CanonicalProviderKey(rawKey)
		b, _ := providerspec.Builtin(key)
		rt := ProviderRuntime{
			Key:        key,
			Backend:    pc.Backend,
			Executable: strings.TrimSpace(pc.Executable),
			CLI:        cloneCLISpec(b.CLI),
		}
		if b.API != nil {
			rt.API = *b.API
		}
		if p := strings.TrimSpace(pc.API.Protocol); p != "" {
			rt.API.Protocol = providerspec.APIProtocol(p)
		}
		if v := strings.TrimSpace(pc.API.BaseURL); v != "" {
			rt.API.DefaultBaseURL = v
		}
		if v := strings.TrimSpace(pc.API.Path); v != "" {
			rt.API.DefaultPath = v
		}
		if v := strings.TrimSpace(pc.API.APIKeyEnv); v != "" {
			rt.API.DefaultAPIKeyEnv = v
		}
		if v := strings.TrimSpace(pc.API.ProviderOptionsKey); v != "" {
			rt.API.ProviderOptionsKey = v
		}
		if v := strings.TrimSpace(pc.API.ProfileFamily); v != "" {
			rt.API.ProfileFamily = v
		}
		rt.apiHeaders = cloneStringMap(pc.API.Headers)
		rt.ProfileFamily = rt.API.ProfileFamily
		if len(pc.Failover) > 0 {
			rt.Failover = providerspec.CanonicalizeProviderList(pc.Failover)
		} else if len(b.Failover) > 0 {
			rt.Failover = providerspec.CanonicalizeProviderList(b.Failover)
		}
		out[key] = rt
	}
	return out, nil
}

func cloneCLISpec(in *providerspec.CLISpec) *providerspec.CLISpec {
	if in == nil {
		return nil
	}
	cp := *in
	cp.InvocationTemplate = append([]string{}, in.InvocationTemplate...)
	cp.HelpProbeArgs = append([]string{}, in.HelpProbeArgs...)
	cp.CapabilityAll = append([]string{}, in.CapabilityAll...)
	if len(in.CapabilityAnyOf) > 0 {
		cp.CapabilityAnyOf = make([][]string, 0, len(in.CapabilityAnyOf))
		for _, group := range in.CapabilityAnyOf {
			cp.CapabilityAnyOf = append(cp.CapabilityAnyOf, append([]string{}, group...))
		}
	}
	return &cp
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestResolveProviderRuntimes_MergesBuiltinAndConfigOverrides -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_runtime.go internal/attractor/engine/provider_runtime_test.go
git commit -m "feat(engine): resolve runtime provider definitions from builtin specs and config overrides"
```

### Task 4: Refactor API Client Construction to Protocol Factories

**Files:**
- Create: `internal/llmclient/from_runtime.go`
- Create: `internal/llmclient/from_runtime_test.go`
- Modify: `internal/llm/providers/openai/adapter.go`
- Modify: `internal/llm/providers/anthropic/adapter.go`
- Modify: `internal/llm/providers/google/adapter.go`
- Test: `internal/llm/providers/openai/adapter_test.go`
- Test: `internal/llm/providers/anthropic/adapter_test.go`
- Test: `internal/llm/providers/google/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestNewFromProviderRuntimes_RegistersAdaptersByProtocol(t *testing.T) {
	runtimes := map[string]engine.ProviderRuntime{
		"openai": {Key: "openai", Backend: engine.BackendAPI, API: providerspec.APISpec{Protocol: providerspec.ProtocolOpenAIResponses, DefaultBaseURL: "http://127.0.0.1:0", DefaultAPIKeyEnv: "OPENAI_API_KEY", ProviderOptionsKey: "openai"}},
	}
	t.Setenv("OPENAI_API_KEY", "test-key")
	c, err := NewFromProviderRuntimes(runtimes)
	if err != nil {
		t.Fatalf("NewFromProviderRuntimes: %v", err)
	}
	if len(c.ProviderNames()) != 1 {
		t.Fatalf("expected one adapter")
	}
}

func TestOpenAIAdapter_NewWithProvider_UsesConfiguredName(t *testing.T) {
	a := openai.NewWithProvider("kimi", "k", "https://api.example.com")
	if got := a.Name(); got != "kimi" {
		t.Fatalf("Name()=%q want kimi", got)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llmclient ./internal/llm/providers/openai ./internal/llm/providers/anthropic ./internal/llm/providers/google -run 'TestNewFromProviderRuntimes_RegistersAdaptersByProtocol|TestOpenAIAdapter_NewWithProvider_UsesConfiguredName' -v`
Expected: FAIL (`NewFromProviderRuntimes` undefined)

**Step 3: Write minimal implementation**

```go
func NewFromProviderRuntimes(runtimes map[string]engine.ProviderRuntime) (*llm.Client, error) {
	c := llm.NewClient()
	keys := sortedKeys(runtimes)
	for _, key := range keys {
		rt := runtimes[key]
		if rt.Backend != engine.BackendAPI {
			continue
		}
		apiKey := strings.TrimSpace(os.Getenv(rt.API.DefaultAPIKeyEnv))
		if apiKey == "" {
			continue
		}
		switch rt.API.Protocol {
		case providerspec.ProtocolOpenAIResponses:
			c.Register(openai.NewWithProvider(key, apiKey, rt.API.DefaultBaseURL))
		case providerspec.ProtocolAnthropicMessages:
			c.Register(anthropic.NewWithProvider(key, apiKey, rt.API.DefaultBaseURL))
		case providerspec.ProtocolGoogleGenerateContent:
			c.Register(google.NewWithProvider(key, apiKey, rt.API.DefaultBaseURL))
		case providerspec.ProtocolOpenAIChatCompletions:
			c.Register(openaicompat.NewAdapter(openaicompat.Config{Provider: key, APIKey: apiKey, BaseURL: rt.API.DefaultBaseURL, Path: rt.API.DefaultPath, OptionsKey: rt.API.ProviderOptionsKey, ExtraHeaders: rt.APIHeaders()}))
		default:
			return nil, fmt.Errorf("unsupported api protocol %q for provider %s", rt.API.Protocol, key)
		}
	}
	if len(c.ProviderNames()) == 0 {
		return nil, fmt.Errorf("no API providers configured from run config/env (providers may be CLI-only)")
	}
	return c, nil
}
```

```go
// openai/adapter.go (same constructor pattern for anthropic/google)
type Adapter struct {
	Provider string
	APIKey   string
	BaseURL  string
	Client   *http.Client
}

func NewWithProvider(provider, apiKey, baseURL string) *Adapter {
	p := providerspec.CanonicalProviderKey(provider)
	if p == "" {
		p = "openai"
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = "https://api.openai.com"
	}
	return &Adapter{
		Provider: p,
		APIKey:   strings.TrimSpace(apiKey),
		BaseURL:  base,
		Client:   &http.Client{Timeout: 0},
	}
}

func NewFromEnv() (*Adapter, error) {
	key := strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
	if key == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is required")
	}
	return NewWithProvider("openai", key, os.Getenv("OPENAI_BASE_URL")), nil
}

func (a *Adapter) Name() string { return a.Provider }
```

Apply the same constructor/name pattern in:
- `internal/llm/providers/anthropic/adapter.go` using default base URL `https://api.anthropic.com`
- `internal/llm/providers/google/adapter.go` using default base URL `https://generativelanguage.googleapis.com`

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llmclient ./internal/llm/providers/openai ./internal/llm/providers/anthropic ./internal/llm/providers/google -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/llmclient/from_runtime.go internal/llmclient/from_runtime_test.go internal/llm/providers/openai/adapter.go internal/llm/providers/openai/adapter_test.go internal/llm/providers/anthropic/adapter.go internal/llm/providers/anthropic/adapter_test.go internal/llm/providers/google/adapter.go internal/llm/providers/google/adapter_test.go
git commit -m "refactor(llmclient): construct API adapters from runtime provider protocol metadata"
```

### Task 5: Implement Generic OpenAI Chat Completions Adapter

**Files:**
- Create: `internal/llm/providers/openaicompat/adapter.go`
- Test: `internal/llm/providers/openaicompat/adapter_test.go`

**Step 1: Write the failing test**

```go
func TestAdapter_Complete_ChatCompletionsMapsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"c1","model":"m","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"README.md\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, Path: "/v1/chat/completions", OptionsKey: "kimi"})
	resp, err := a.Complete(context.Background(), llm.Request{Provider: "kimi", Model: "kimi-k2.5", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("tool call mapping failed")
	}
}

func TestAdapter_Stream_EmitsFinishEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"c2","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "zai", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{Provider: "zai", Model: "glm-4.7", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	sawFinish := false
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish {
			sawFinish = true
			break
		}
	}
	if !sawFinish {
		t.Fatalf("expected finish event")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/llm/providers/openaicompat -run 'TestAdapter_Complete_ChatCompletionsMapsToolCalls|TestAdapter_Stream_EmitsFinishEvent' -v`
Expected: FAIL (package/adapter missing)

**Step 3: Write minimal implementation**

```go
type Config struct {
	Provider     string
	APIKey       string
	BaseURL      string
	Path         string
	OptionsKey   string
	ExtraHeaders map[string]string
}

type Adapter struct {
	cfg    Config
	client *http.Client
}

func NewAdapter(cfg Config) *Adapter {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if strings.TrimSpace(cfg.Path) == "" {
		cfg.Path = "/v1/chat/completions"
	}
	if strings.TrimSpace(cfg.OptionsKey) == "" {
		cfg.OptionsKey = strings.TrimSpace(cfg.Provider)
	}
	if cfg.Provider == "" {
		cfg.Provider = cfg.OptionsKey
	}
	// Intentionally no client timeout; request contexts control cancellation/deadlines.
	return &Adapter{cfg: cfg, client: &http.Client{Timeout: 0}}
}

func (a *Adapter) Name() string { return a.cfg.Provider }

func (a *Adapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	body := toChatCompletionsBody(req, a.cfg.OptionsKey)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.BaseURL+a.cfg.Path, bytes.NewReader(body))
	if err != nil {
		return llm.Response{}, llm.WrapContextError(a.cfg.Provider, err)
	}
	httpReq.Header.Set("Authorization", "Bearer "+a.cfg.APIKey)
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range a.cfg.ExtraHeaders {
		httpReq.Header.Set(k, v)
	}
	resp, err := a.client.Do(httpReq)
	if err != nil {
		return llm.Response{}, llm.WrapContextError(a.cfg.Provider, err)
	}
	defer resp.Body.Close()
	return parseChatCompletionsResponse(a.cfg.Provider, req.Model, resp)
}

func (a *Adapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	sctx, cancel := context.WithCancel(ctx)
	s := llm.NewChanStream(cancel)
	go func() {
		defer s.Close()
		s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
		resp, err := a.Complete(sctx, req)
		if err != nil {
			s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError(a.cfg.Provider, err.Error())})
			return
		}
		if txt := resp.Text(); txt != "" {
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: "assistant_text"})
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "assistant_text", Delta: txt})
			s.Send(llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: "assistant_text"})
		}
		for _, tc := range resp.ToolCalls() {
			call := tc
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &call})
			s.Send(llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &call})
		}
		r := resp
		s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &r.Finish, Usage: &r.Usage, Response: &r})
	}()
	return s, nil
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/llm/providers/openaicompat -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/llm/providers/openaicompat/adapter.go internal/llm/providers/openaicompat/adapter_test.go
git commit -m "feat(llm): add generic OpenAI Chat Completions adapter for protocol-based providers"
```

### Task 6: Refactor API Routing, Agent Profile Selection, and Failover to Runtime Metadata

**Files:**
- Modify: `internal/attractor/engine/codergen_router.go`
- Modify: `internal/attractor/engine/run_with_config.go`
- Create: `internal/agent/profile_registry.go`
- Test: `internal/agent/profile_test.go`
- Test: `internal/attractor/engine/codergen_failover_test.go`

**Step 1: Write the failing test**

```go
func TestProfileForProvider_UsesConfiguredProfileFamily(t *testing.T) {
	p, err := agent.NewProfileForFamily("openai", "glm-4.7")
	if err != nil {
		t.Fatalf("NewProfileForFamily: %v", err)
	}
	if p.ID() != "openai" {
		t.Fatalf("expected openai family profile")
	}
}

func TestFailoverOrder_UsesRuntimeProviderPolicy(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"kimi": {Key: "kimi", Failover: []string{"zai", "openai"}},
	}
	got := failoverOrderFromRuntime("kimi", rt)
	if strings.Join(got, ",") != "zai,openai" {
		t.Fatalf("failover mismatch: %v", got)
	}
}

func TestPickFailoverModelFromRuntime_NeverReturnsEmptyForConfiguredProvider(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"zai": {Key: "zai"},
	}
	got := pickFailoverModelFromRuntime("zai", rt, nil, "glm-4.7")
	if got == "" {
		t.Fatalf("expected non-empty failover model")
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/agent ./internal/attractor/engine -run 'TestProfileForProvider_UsesConfiguredProfileFamily|TestFailoverOrder_UsesRuntimeProviderPolicy|TestPickFailoverModelFromRuntime_NeverReturnsEmptyForConfiguredProvider' -v`
Expected: FAIL (`NewProfileForFamily` / `failoverOrderFromRuntime` / `pickFailoverModelFromRuntime` missing)

**Step 3: Write minimal implementation**

```go
// internal/agent/profile_registry.go
var profileFactories = map[string]func(string) ProviderProfile{
	"openai":    NewOpenAIProfile,
	"anthropic": NewAnthropicProfile,
	"google":    NewGeminiProfile,
}

func NewProfileForFamily(family string, model string) (ProviderProfile, error) {
	f := strings.ToLower(strings.TrimSpace(family))
	factory, ok := profileFactories[f]
	if !ok {
		return nil, fmt.Errorf("unsupported profile family: %s", family)
	}
	return factory(model), nil
}
```

```go
// codergen_router.go (usage)
profile, err := agent.NewProfileForFamily(runtimeProvider.ProfileFamily, mid)
...
func failoverOrderFromRuntime(primary string, rt map[string]ProviderRuntime) []string {
	p := providerspec.CanonicalProviderKey(primary)
	if r, ok := rt[p]; ok && len(r.Failover) > 0 {
		return append([]string{}, r.Failover...)
	}
	return nil
}

func pickFailoverModelFromRuntime(provider string, rt map[string]ProviderRuntime, catalog *modeldb.LiteLLMCatalog, fallbackModel string) string {
	p := providerspec.CanonicalProviderKey(provider)
	for _, cand := range []string{
		strings.TrimSpace(fallbackModel),
		pickFailoverModel(p, catalog),
		bestModelForProvider(catalog, p),
	} {
		if strings.TrimSpace(cand) != "" {
			return strings.TrimSpace(cand)
		}
	}
	return fallbackModel
}
```

Compatibility contract:
- Kimi/Z use `profile_family: openai`.
- Task 5 verifies OpenAI-style tool-call decoding on Kimi payloads.
- Task 9 verifies end-to-end transport/path behavior for both Kimi and Z.

**Step 4: Run test to verify it passes**

Run: `go test ./internal/agent ./internal/attractor/engine -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/agent/profile_registry.go internal/agent/profile_test.go internal/attractor/engine/codergen_router.go internal/attractor/engine/codergen_failover_test.go internal/attractor/engine/run_with_config.go
git commit -m "refactor(engine): drive API profile selection and failover from runtime provider metadata"
```

### Task 7: Refactor CLI Execution and Preflight to CLI Contracts

**Files:**
- Modify: `internal/attractor/engine/provider_exec_policy.go`
- Modify: `internal/attractor/engine/provider_preflight.go`
- Modify: `internal/attractor/engine/provider_error_classification.go`
- Modify: `internal/attractor/engine/codergen_router.go`
- Test: `internal/attractor/engine/provider_preflight_test.go`
- Test: `internal/attractor/engine/provider_exec_policy_test.go`
- Test: `internal/attractor/engine/provider_error_classification_test.go`

**Step 1: Write the failing test**

```go
func TestDefaultCLIInvocation_UsesSpecTemplate(t *testing.T) {
	spec := providerspec.CLISpec{DefaultExecutable: "mycli", InvocationTemplate: []string{"run", "--model", "{{model}}", "--cwd", "{{worktree}}", "--prompt", "{{prompt}}"}}
	exe, args := materializeCLIInvocation(spec, "m1", "/tmp/w", "fix bug")
	if exe != "mycli" || strings.Join(args, " ") != "run --model m1 --cwd /tmp/w --prompt fix bug" {
		t.Fatalf("materialization mismatch: exe=%s args=%v", exe, args)
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestDefaultCLIInvocation_UsesSpecTemplate -v`
Expected: FAIL (`materializeCLIInvocation` undefined)

**Step 3: Write minimal implementation**

```go
func materializeCLIInvocation(spec providerspec.CLISpec, modelID, worktree, prompt string) (string, []string) {
	exe := strings.TrimSpace(spec.DefaultExecutable)
	args := make([]string, 0, len(spec.InvocationTemplate))
	for _, token := range spec.InvocationTemplate {
		repl := strings.ReplaceAll(token, "{{model}}", modelID)
		repl = strings.ReplaceAll(repl, "{{worktree}}", worktree)
		repl = strings.ReplaceAll(repl, "{{prompt}}", prompt)
		args = append(args, repl)
	}
	return exe, args
}
```

```go
// provider_preflight.go
func missingCapabilityTokensFromSpec(spec *providerspec.CLISpec, helpOutput string) []string { ... }
func probeOutputLooksLikeHelpFromSpec(spec *providerspec.CLISpec, output string) bool { ... }
```

```go
// provider_error_classification.go
func classifyProviderCLIErrorWithContract(provider string, spec *providerspec.CLISpec, stderr string, runErr error) providerCLIClassifiedError { ... }
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/provider_exec_policy.go internal/attractor/engine/provider_preflight.go internal/attractor/engine/provider_error_classification.go internal/attractor/engine/codergen_router.go internal/attractor/engine/provider_preflight_test.go internal/attractor/engine/provider_exec_policy_test.go internal/attractor/engine/provider_error_classification_test.go
git commit -m "refactor(engine-cli): replace provider-name switches with CLI contract metadata"
```

### Task 8: Wire Kimi and Z as API-Only Providers End-to-End

**Files:**
- Modify: `internal/attractor/engine/run_with_config.go`
- Modify: `internal/attractor/engine/provider_preflight.go`
- Test: `internal/attractor/engine/run_with_config_test.go`
- Test: `internal/attractor/engine/provider_preflight_test.go`

**Step 1: Write the failing test**

```go
func TestRunWithConfig_AcceptsKimiAndZaiAPIProviders(t *testing.T) {
	cases := []struct {
		provider string
		model    string
		keyEnv   string
		path     string
	}{
		{provider: "kimi", model: "kimi-k2.5", keyEnv: "KIMI_API_KEY", path: "/v1/chat/completions"},
		{provider: "zai", model: "glm-4.7", keyEnv: "ZAI_API_KEY", path: "/api/paas/v4/chat/completions"},
	}

	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			dot := []byte(fmt.Sprintf(`digraph G { a [shape=box, llm_provider=%s, llm_model=%s, prompt="hi"] }`, tc.provider, tc.model))
			cfg := &RunConfigFile{Version: 1}
			cfg.Repo.Path = "/tmp/repo"
			cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
			cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
			cfg.ModelDB.LiteLLMCatalogPath = "/tmp/catalog.json"
			cfg.LLM.Providers = map[string]ProviderConfig{
				tc.provider: {Backend: BackendAPI, API: ProviderAPIConfig{Protocol: "openai_chat_completions", APIKeyEnv: tc.keyEnv, BaseURL: "http://127.0.0.1:1", Path: tc.path, ProfileFamily: "openai"}},
			}
			t.Setenv(tc.keyEnv, "k-test")
			_, err := RunWithConfig(context.Background(), dot, cfg, RunOptions{RunID: "r1-" + tc.provider, LogsRoot: t.TempDir()})
			if err == nil {
				t.Fatalf("expected transport error from fake endpoint, got nil")
			}
			if strings.Contains(err.Error(), "unsupported provider") {
				t.Fatalf("provider should be accepted, got %v", err)
			}
		})
	}
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_AcceptsKimiAndZaiAPIProviders -v`
Expected: FAIL (still rejects unknown providers)

**Step 3: Write minimal implementation**

```go
// run_with_config.go
runtimes, err := resolveProviderRuntimes(cfg)
if err != nil { return nil, err }
eng.CodergenBackend = NewCodergenRouterWithRuntimes(cfg, catalog, runtimes)
```

```go
// provider_preflight.go
if rt[provider].Backend == BackendCLI {
	// CLI checks as before
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestRunWithConfig_AcceptsKimiAndZaiAPIProviders -v`
Expected: PASS (or deterministic network failure not provider validation failure)

**Step 5: Commit**

```bash
git add internal/attractor/engine/run_with_config.go internal/attractor/engine/provider_preflight.go internal/attractor/engine/run_with_config_test.go internal/attractor/engine/provider_preflight_test.go
git commit -m "feat(engine): accept kimi and zai API providers via runtime provider configuration"
```

### Task 9: Add Integration Tests for Kimi and Z API Protocols

**Files:**
- Create: `internal/attractor/engine/kimi_zai_api_integration_test.go`

**Step 1: Write the failing test**

```go
func TestKimiAndZai_OpenAIChatCompletionsIntegration(t *testing.T) {
	var seenPaths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenPaths = append(seenPaths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	// configure kimi and zai providers, run tiny graph for each, assert paths observed
	// kimi path: /v1/chat/completions
	// zai path: /api/paas/v4/chat/completions
}
```

**Step 2: Run test to verify it fails**

Run: `go test ./internal/attractor/engine -run TestKimiAndZai_OpenAIChatCompletionsIntegration -v`
Expected: FAIL (new integration test not yet implemented)

**Step 3: Write minimal implementation**

```go
func TestKimiAndZai_OpenAIChatCompletionsIntegration(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	seenPaths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPaths[r.URL.Path]++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	runCase := func(provider, model, keyEnv, path string) {
		t.Helper()
		cfg := &RunConfigFile{Version: 1}
		cfg.Repo.Path = repo
		cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
		cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
		cfg.ModelDB.LiteLLMCatalogPath = pinned
		cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
		cfg.Git.RunBranchPrefix = "attractor/run"
		cfg.LLM.Providers = map[string]ProviderConfig{
			provider: {Backend: BackendAPI, API: ProviderAPIConfig{
				Protocol:      "openai_chat_completions",
				APIKeyEnv:     keyEnv,
				BaseURL:       srv.URL,
				Path:          path,
				ProfileFamily: "openai",
			}},
		}
		t.Setenv(keyEnv, "k")

		dot := []byte(fmt.Sprintf(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=%s, llm_model=%s, codergen_mode=one_shot, prompt="say hi"]
  start -> a -> exit
}
`, provider, model))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "kz-" + provider, LogsRoot: logsRoot})
		if err != nil {
			t.Fatalf("%s run failed: %v", provider, err)
		}
	}

	runCase("kimi", "kimi-k2.5", "KIMI_API_KEY", "/v1/chat/completions")
	runCase("zai", "glm-4.7", "ZAI_API_KEY", "/api/paas/v4/chat/completions")

	mu.Lock()
	defer mu.Unlock()
	if seenPaths["/v1/chat/completions"] == 0 {
		t.Fatalf("missing kimi chat-completions call: %v", seenPaths)
	}
	if seenPaths["/api/paas/v4/chat/completions"] == 0 {
		t.Fatalf("missing zai chat-completions call: %v", seenPaths)
	}
}
```

**Step 4: Run test to verify it passes**

Run: `go test ./internal/attractor/engine -run TestKimiAndZai_OpenAIChatCompletionsIntegration -v`
Expected: PASS

**Step 5: Commit**

```bash
git add internal/attractor/engine/kimi_zai_api_integration_test.go
git commit -m "test(engine): add end-to-end api integration coverage for kimi and zai chat-completions providers"
```

### Task 10: Update Docs, Examples, and Migration Notes

**Files:**
- Modify: `README.md`
- Modify: `docs/strongdm/attractor/README.md`
- Modify: `docs/strongdm/attractor/kilroy-metaspec.md`
- Create: `docs/strongdm/attractor/provider-plugin-migration.md`

**Step 1: Write the failing test (docs lint/consistency check)**

```bash
rg -n "unsupported provider in config|supported providers[^\\n]*openai[^\\n]*anthropic[^\\n]*google|openai\\|anthropic\\|google only" README.md docs/strongdm/attractor/*.md
```

Expected: existing hard-coded wording still present

**Step 2: Run docs check to verify mismatch exists**

Run: `rg -n "supported providers|provider plug-in|protocol" README.md docs/strongdm/attractor/*.md`
Expected: lines requiring update found

**Step 3: Write minimal documentation updates**

```yaml
llm:
  providers:
    kimi:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: KIMI_API_KEY
        base_url: https://api.moonshot.ai
        path: /v1/chat/completions
        profile_family: openai
    zai:
      backend: api
      api:
        protocol: openai_chat_completions
        api_key_env: ZAI_API_KEY
        base_url: https://api.z.ai
        path: /api/paas/v4/chat/completions
        profile_family: openai
```

**Step 4: Run docs check to verify it passes**

Run: `rg -n "unsupported provider in config|openai\|anthropic\|google only" README.md docs/strongdm/attractor/*.md`
Expected: no stale hard-coded-provider claim remains (`rg` exits non-zero)

**Step 5: Commit**

```bash
git add README.md docs/strongdm/attractor/README.md docs/strongdm/attractor/kilroy-metaspec.md docs/strongdm/attractor/provider-plugin-migration.md
git commit -m "docs(attractor): document provider plugin schema and kimi/zai api-only configuration"
```

### Task 11: Final Verification and Safety Regression Sweep

**Files:**
- Modify (if needed): affected tests/docs from previous tasks

**Step 1: Write failing regression test for compatibility (if missing)**

```go
func TestBackwardCompatibility_OpenAIAnthropicGoogleStillValid(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = "/tmp/repo"
	cfg.CXDB.BinaryAddr = "127.0.0.1:9009"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:9010"
	cfg.ModelDB.LiteLLMCatalogPath = "/tmp/catalog.json"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai":    {Backend: BackendAPI},
		"anthropic": {Backend: BackendAPI},
		"google":    {Backend: BackendAPI},
	}
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("unexpected validation error: %v", err)
	}
}
```

**Step 2: Run test to verify it fails (if behavior regressed)**

Run: `go test ./internal/attractor/engine -run TestBackwardCompatibility_OpenAIAnthropicGoogleStillValid -v`
Expected: PASS after fixes (if FAIL, fix before final commit)

**Step 3: Run focused and broad test suites**

Run: `go test ./internal/providerspec ./internal/llm/... ./internal/llmclient ./internal/agent ./internal/attractor/engine -count=1`
Expected: PASS

**Step 4: Run formatting/lint checks used by repo**

Run: `go test ./...`
Expected: PASS

**Step 5: Final commit**

```bash
git add internal/providerspec internal/llm/providers/openaicompat internal/llm/providers/openai internal/llm/providers/anthropic internal/llm/providers/google internal/llmclient internal/agent internal/attractor/engine README.md docs/strongdm/attractor
git commit -m "refactor(attractor): introduce protocol-driven provider plugin architecture and add kimi/zai api support"
```

---

## Notes for Execution

- Keep changes backward compatible until Task 11 (do not break existing `openai/anthropic/google` runs mid-refactor).
- Prefer incremental adapters and wrapper constructors over rewriting all provider code in one commit.
- For API-only rollout, Kimi and Z should be configured with `backend: api`; do not add CLI mappings for them in this pass unless explicitly requested.
- If any task requires unexpected spec decisions (for example custom auth headers beyond bearer), pause and record decision in `docs/strongdm/attractor/provider-plugin-migration.md` before continuing.
