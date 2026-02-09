package engine

import "testing"

func TestResolveProviderRuntimes_MergesBuiltinAndConfigOverrides(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				APIKeyEnv: "KIMI_API_KEY",
				Headers:   map[string]string{"X-Trace": "1"},
			},
		},
	}

	rt, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	if rt["kimi"].API.Protocol != "anthropic_messages" {
		t.Fatalf("kimi protocol mismatch")
	}
	if _, ok := rt["openai"]; !ok {
		t.Fatalf("expected failover target runtime for openai")
	}
	if rt["openai"].API.DefaultPath != "/v1/responses" {
		t.Fatalf("expected synthesized openai default path")
	}
	if got := rt["kimi"].APIHeaders(); got["X-Trace"] != "1" {
		t.Fatalf("expected runtime headers copy, got %v", got)
	}
}

func TestResolveProviderRuntimes_RejectsCanonicalAliasCollisions(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"zai": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				Protocol:  "openai_chat_completions",
				APIKeyEnv: "ZAI_API_KEY",
			},
		},
		"z-ai": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				Protocol:  "openai_chat_completions",
				APIKeyEnv: "ZAI_API_KEY",
			},
		},
	}

	_, err := resolveProviderRuntimes(cfg)
	if err == nil {
		t.Fatalf("expected canonical collision error, got nil")
	}
	const want = `duplicate provider config after canonicalization: "z-ai" and "zai" both map to "zai"`
	if err.Error() != want {
		t.Fatalf("expected canonical collision error, got %v", err)
	}
}
