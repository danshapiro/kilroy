package providerspec

import "testing"

func TestBuiltinSpecsIncludeCoreAndNewProviders(t *testing.T) {
	s := Builtins()
	for _, key := range []string{"openai", "codex-app-server", "anthropic", "google", "kimi", "zai", "cerebras", "minimax", "inception"} {
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
	if got := CanonicalProviderKey("moonshotai"); got != "kimi" {
		t.Fatalf("moonshotai alias: got %q want %q", got, "kimi")
	}
	if got := CanonicalProviderKey("google_ai_studio"); got != "google" {
		t.Fatalf("google_ai_studio alias: got %q want %q", got, "google")
	}
	if got := CanonicalProviderKey("cerebras-ai"); got != "cerebras" {
		t.Fatalf("cerebras-ai alias: got %q want %q", got, "cerebras")
	}
	if got := CanonicalProviderKey("minimax-ai"); got != "minimax" {
		t.Fatalf("minimax-ai alias: got %q want %q", got, "minimax")
	}
	if got := CanonicalProviderKey("codex_app_server"); got != "codex-app-server" {
		t.Fatalf("codex_app_server alias: got %q want %q", got, "codex-app-server")
	}
	if got := CanonicalProviderKey("inceptionlabs"); got != "inception" {
		t.Fatalf("inceptionlabs alias: got %q want %q", got, "inception")
	}
	if got := CanonicalProviderKey("inception-labs"); got != "inception" {
		t.Fatalf("inception-labs alias: got %q want %q", got, "inception")
	}
	if got := CanonicalProviderKey("glm"); got != "glm" {
		t.Fatalf("unknown provider keys should pass through unchanged, got %q", got)
	}
}

func TestBuiltinCodexAppServerDefaults(t *testing.T) {
	spec, ok := Builtin("codex-app-server")
	if !ok {
		t.Fatalf("expected codex-app-server builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected codex-app-server api spec")
	}
	if got := spec.API.Protocol; got != ProtocolCodexAppServer {
		t.Fatalf("codex-app-server protocol: got %q want %q", got, ProtocolCodexAppServer)
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "" {
		t.Fatalf("codex-app-server api_key_env: got %q want empty", got)
	}
	if got := spec.API.ProviderOptionsKey; got != "codex_app_server" {
		t.Fatalf("codex-app-server provider_options_key: got %q want %q", got, "codex_app_server")
	}
	if got := spec.API.ProfileFamily; got != "codex-app-server" {
		t.Fatalf("codex-app-server profile_family: got %q want %q", got, "codex-app-server")
	}
}

func TestBuiltinCerebrasDefaultsToOpenAICompatAPI(t *testing.T) {
	spec, ok := Builtin("cerebras")
	if !ok {
		t.Fatalf("expected cerebras builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected cerebras api spec")
	}
	if got := spec.API.Protocol; got != ProtocolOpenAIChatCompletions {
		t.Fatalf("cerebras protocol: got %q want %q", got, ProtocolOpenAIChatCompletions)
	}
	if got := spec.API.DefaultBaseURL; got != "https://api.cerebras.ai" {
		t.Fatalf("cerebras base url: got %q want %q", got, "https://api.cerebras.ai")
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "CEREBRAS_API_KEY" {
		t.Fatalf("cerebras api_key_env: got %q want %q", got, "CEREBRAS_API_KEY")
	}
}

func TestBuiltinKimiDefaultsToCodingAnthropicAPI(t *testing.T) {
	spec, ok := Builtin("kimi")
	if !ok {
		t.Fatalf("expected kimi builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected kimi api spec")
	}
	if got := spec.API.Protocol; got != ProtocolAnthropicMessages {
		t.Fatalf("kimi protocol: got %q want %q", got, ProtocolAnthropicMessages)
	}
	if got := spec.API.DefaultBaseURL; got != "https://api.kimi.com/coding" {
		t.Fatalf("kimi base url: got %q want %q", got, "https://api.kimi.com/coding")
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "KIMI_API_KEY" {
		t.Fatalf("kimi api_key_env: got %q want %q", got, "KIMI_API_KEY")
	}
}

func TestBuiltinMinimaxDefaultsToOpenAICompatAPI(t *testing.T) {
	spec, ok := Builtin("minimax")
	if !ok {
		t.Fatalf("expected minimax builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected minimax api spec")
	}
	if got := spec.API.Protocol; got != ProtocolOpenAIChatCompletions {
		t.Fatalf("minimax protocol: got %q want %q", got, ProtocolOpenAIChatCompletions)
	}
	if got := spec.API.DefaultBaseURL; got != "https://api.minimax.io" {
		t.Fatalf("minimax base url: got %q want %q", got, "https://api.minimax.io")
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "MINIMAX_API_KEY" {
		t.Fatalf("minimax api_key_env: got %q want %q", got, "MINIMAX_API_KEY")
	}
}

func TestBuiltinInceptionDefaultsToOpenAICompatAPI(t *testing.T) {
	spec, ok := Builtin("inception")
	if !ok {
		t.Fatalf("expected inception builtin")
	}
	if spec.API == nil {
		t.Fatalf("expected inception api spec")
	}
	if got := spec.API.Protocol; got != ProtocolOpenAIChatCompletions {
		t.Fatalf("inception protocol: got %q want %q", got, ProtocolOpenAIChatCompletions)
	}
	if got := spec.API.DefaultBaseURL; got != "https://api.inceptionlabs.ai" {
		t.Fatalf("inception base url: got %q want %q", got, "https://api.inceptionlabs.ai")
	}
	if got := spec.API.DefaultAPIKeyEnv; got != "INCEPTION_API_KEY" {
		t.Fatalf("inception api_key_env: got %q want %q", got, "INCEPTION_API_KEY")
	}
}

func TestBuiltinFailoverDefaultsAreEmpty(t *testing.T) {
	for _, provider := range []string{"openai", "anthropic", "google", "kimi", "zai", "cerebras", "minimax", "inception"} {
		spec, ok := Builtin(provider)
		if !ok {
			t.Fatalf("expected builtin provider %q", provider)
		}
		if len(spec.Failover) != 0 {
			t.Fatalf("%s failover=%v want []", provider, spec.Failover)
		}
	}
}
