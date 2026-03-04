package providerspec

import (
	"strings"
	"sync"
)

type APIProtocol string

const (
	ProtocolOpenAIResponses       APIProtocol = "openai_responses"
	ProtocolOpenAIChatCompletions APIProtocol = "openai_chat_completions"
	ProtocolAnthropicMessages     APIProtocol = "anthropic_messages"
	ProtocolGoogleGenerateContent APIProtocol = "google_generate_content"
	ProtocolCodexAppServer        APIProtocol = "codex_app_server"
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
	DefaultExecutable  string
	InvocationTemplate []string
	PromptMode         string
	HelpProbeArgs      []string
	CapabilityAll      []string
	CapabilityAnyOf    [][]string
}

type Spec struct {
	Key      string
	Aliases  []string
	API      *APISpec
	CLI      *CLISpec
	Failover []string
}

var (
	providerAliasOnce  sync.Once
	providerAliasIndex map[string]string
)

func providerAliases() map[string]string {
	providerAliasOnce.Do(func() {
		providerAliasIndex = providerAliasIndexFromBuiltins(Builtins())
	})
	return providerAliasIndex
}

func providerAliasIndexFromBuiltins(specs map[string]Spec) map[string]string {
	out := map[string]string{}
	for rawKey, spec := range specs {
		key := strings.ToLower(strings.TrimSpace(rawKey))
		if key == "" {
			continue
		}
		out[key] = key
		for _, rawAlias := range spec.Aliases {
			alias := strings.ToLower(strings.TrimSpace(rawAlias))
			if alias != "" {
				out[alias] = key
			}
		}
	}
	return out
}

func CanonicalProviderKey(in string) string {
	key := strings.ToLower(strings.TrimSpace(in))
	if key == "" {
		return ""
	}
	if canonical, ok := providerAliases()[key]; ok {
		return canonical
	}
	return key
}

func CanonicalizeProviderList(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := map[string]struct{}{}
	for _, raw := range in {
		key := CanonicalProviderKey(raw)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
