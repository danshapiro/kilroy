// Unit tests for provider auto-detection from environment.

package engine

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

func TestDetectProviders_AnthropicAPIKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-123")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	// No CLI binary on path — should fall back to API backend.
	detected := detectProvidersWithLookPath(func(name string) (string, error) {
		return "", fmt.Errorf("not found: %s", name)
	})
	var found *DetectedProvider
	for i := range detected {
		if detected[i].Key == "anthropic" {
			found = &detected[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected anthropic provider to be detected")
	}
	if found.Backend != BackendAPI {
		t.Fatalf("expected api backend, got %q", found.Backend)
	}
}

func TestDetectProviders_CLIPreferred(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-123")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")

	// Simulate claude binary on path.
	detected := detectProvidersWithLookPath(func(name string) (string, error) {
		if name == "claude" {
			return "/usr/bin/claude", nil
		}
		return "", fmt.Errorf("not found: %s", name)
	})
	var found *DetectedProvider
	for i := range detected {
		if detected[i].Key == "anthropic" {
			found = &detected[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected anthropic provider to be detected")
	}
	if found.Backend != BackendCLI {
		t.Fatalf("expected cli backend when binary found, got %q", found.Backend)
	}
}

func TestDetectProviders_GoogleFallbackKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "goog-test-123")

	detected := detectProvidersWithLookPath(func(name string) (string, error) {
		return "", fmt.Errorf("not found: %s", name)
	})
	var found *DetectedProvider
	for i := range detected {
		if detected[i].Key == "google" {
			found = &detected[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected google provider to be detected via GOOGLE_API_KEY")
	}
}

func TestDetectProviders_NoKeysNoResults(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	t.Setenv("ZAI_API_KEY", "")
	t.Setenv("CEREBRAS_API_KEY", "")
	t.Setenv("MINIMAX_API_KEY", "")
	t.Setenv("INCEPTION_API_KEY", "")

	detected := detectProvidersWithLookPath(func(name string) (string, error) {
		return "", fmt.Errorf("not found: %s", name)
	})
	if len(detected) != 0 {
		keys := make([]string, 0, len(detected))
		for _, d := range detected {
			keys = append(keys, d.Key)
		}
		sort.Strings(keys)
		t.Fatalf("expected no providers, got: %s", strings.Join(keys, ", "))
	}
}

func TestApplyDetectedProviders_DoesNotOverwrite(t *testing.T) {
	cfg := &RunConfigFile{}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"anthropic": {Backend: BackendCLI},
	}
	ApplyDetectedProviders(cfg, []DetectedProvider{
		{Key: "anthropic", Backend: BackendAPI},
		{Key: "openai", Backend: BackendAPI},
	})
	if cfg.LLM.Providers["anthropic"].Backend != BackendCLI {
		t.Fatal("existing provider config should not be overwritten")
	}
	if _, ok := cfg.LLM.Providers["openai"]; !ok {
		t.Fatal("new provider should be added")
	}
}
