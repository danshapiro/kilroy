package engine

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/strongdm/kilroy/internal/llm"
	"github.com/strongdm/kilroy/internal/llm/providers/anthropic"
	"github.com/strongdm/kilroy/internal/llm/providers/google"
	"github.com/strongdm/kilroy/internal/llm/providers/openai"
	"github.com/strongdm/kilroy/internal/llm/providers/openaicompat"
	"github.com/strongdm/kilroy/internal/providerspec"
)

func newAPIClientFromProviderRuntimes(runtimes map[string]ProviderRuntime) (*llm.Client, error) {
	c := llm.NewClient()
	for _, key := range sortedKeys(runtimes) {
		rt := runtimes[key]
		if rt.Backend != BackendAPI {
			continue
		}
		apiKey := strings.TrimSpace(os.Getenv(rt.API.DefaultAPIKeyEnv))
		if apiKey == "" {
			continue
		}
		switch rt.API.Protocol {
		case providerspec.ProtocolOpenAIResponses:
			c.Register(openai.NewWithProvider(key, apiKey, resolveBuiltInBaseURLOverride(key, rt.API.DefaultBaseURL)))
		case providerspec.ProtocolAnthropicMessages:
			c.Register(anthropic.NewWithProvider(key, apiKey, resolveBuiltInBaseURLOverride(key, rt.API.DefaultBaseURL)))
		case providerspec.ProtocolGoogleGenerateContent:
			c.Register(google.NewWithProvider(key, apiKey, resolveBuiltInBaseURLOverride(key, rt.API.DefaultBaseURL)))
		case providerspec.ProtocolOpenAIChatCompletions:
			c.Register(openaicompat.NewAdapter(openaicompat.Config{
				Provider:     key,
				APIKey:       apiKey,
				BaseURL:      resolveBuiltInBaseURLOverride(key, rt.API.DefaultBaseURL),
				Path:         rt.API.DefaultPath,
				OptionsKey:   rt.API.ProviderOptionsKey,
				ExtraHeaders: rt.APIHeaders(),
			}))
		default:
			return nil, fmt.Errorf("unsupported api protocol %q for provider %s", rt.API.Protocol, key)
		}
	}
	// Empty API clients are valid (for example, CLI-only runs).
	return c, nil
}

func resolveBuiltInBaseURLOverride(providerKey, defaultBaseURL string) string {
	normalized := strings.TrimSpace(defaultBaseURL)
	switch providerspec.CanonicalProviderKey(providerKey) {
	case "openai":
		if env := strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")); env != "" {
			if normalized == "" || normalized == "https://api.openai.com" {
				return env
			}
		}
	case "anthropic":
		if env := strings.TrimSpace(os.Getenv("ANTHROPIC_BASE_URL")); env != "" {
			if normalized == "" || normalized == "https://api.anthropic.com" {
				return env
			}
		}
	case "google":
		if env := strings.TrimSpace(os.Getenv("GEMINI_BASE_URL")); env != "" {
			if normalized == "" || normalized == "https://generativelanguage.googleapis.com" {
				return env
			}
		}
	case "minimax":
		if env := strings.TrimSpace(os.Getenv("MINIMAX_BASE_URL")); env != "" {
			if normalized == "" || normalized == "https://api.minimax.io" {
				return env
			}
		}
	}
	return normalized
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
