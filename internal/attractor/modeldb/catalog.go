package modeldb

import (
	"strings"

	"github.com/strongdm/kilroy/internal/modelmeta"
)

// Catalog is the normalized, provider-agnostic model metadata snapshot used by
// attractor runtime preflight and routing metadata checks.
type Catalog struct {
	Path   string
	SHA256 string
	Models map[string]ModelEntry
}

type ModelEntry struct {
	Provider string
	Mode     string

	ContextWindow   int
	MaxOutputTokens *int

	SupportsTools     bool
	SupportsVision    bool
	SupportsReasoning bool

	InputCostPerToken  *float64
	OutputCostPerToken *float64
}

// CatalogHasProviderModel returns true when the catalog contains the given
// provider/model pair. It accepts either canonical model IDs
// ("openai/gpt-5.2-codex") or provider-relative IDs ("gpt-5.2-codex").
func CatalogHasProviderModel(c *Catalog, provider, modelID string) bool {
	if c == nil || c.Models == nil {
		return false
	}
	provider = modelmeta.NormalizeProvider(provider)
	modelID = strings.TrimSpace(modelID)
	if provider == "" || modelID == "" {
		return false
	}
	inCanonical := canonicalModelID(provider, modelID)
	inRelative := providerRelativeModelID(provider, modelID)
	for id, entry := range c.Models {
		entryProvider := modelmeta.NormalizeProvider(entry.Provider)
		if entryProvider == "" {
			entryProvider = inferProviderFromModelID(id)
		}
		if entryProvider != provider {
			continue
		}
		if strings.EqualFold(canonicalModelID(provider, id), inCanonical) {
			return true
		}
		if strings.EqualFold(providerRelativeModelID(provider, id), inRelative) {
			return true
		}
	}
	return false
}

func inferProviderFromModelID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	parts := strings.SplitN(id, "/", 2)
	if len(parts) < 2 {
		return ""
	}
	return modelmeta.NormalizeProvider(parts[0])
}

func canonicalModelID(provider string, id string) string {
	provider = modelmeta.NormalizeProvider(provider)
	rel := providerRelativeModelID(provider, id)
	if provider == "" || rel == "" {
		return rel
	}
	return provider + "/" + rel
}

func providerRelativeModelID(provider string, id string) string {
	provider = modelmeta.NormalizeProvider(provider)
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	parts := strings.Split(id, "/")
	if len(parts) == 1 {
		return id
	}
	prefix := modelmeta.NormalizeProvider(parts[0])
	if prefix == provider {
		return strings.TrimSpace(strings.Join(parts[1:], "/"))
	}
	// Legacy LiteLLM keys for Google models often used gemini/<model>.
	if provider == "google" && strings.EqualFold(strings.TrimSpace(parts[0]), "gemini") {
		return strings.TrimSpace(strings.Join(parts[1:], "/"))
	}
	// Legacy LiteLLM keys for Anthropic models may include region prefixes.
	if provider == "anthropic" {
		return strings.TrimSpace(strings.Join(parts[1:], "/"))
	}
	return id
}
