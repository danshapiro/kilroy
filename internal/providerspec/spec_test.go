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
		t.Fatalf("unknown provider keys should pass through unchanged, got %q", got)
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
