package modeldb

import (
	"regexp"
	"strings"

	"github.com/strongdm/kilroy/internal/modelmeta"
)

// versionDotRe matches dots between digits in model version numbers
// (e.g. "4.5", "3.7") without touching other dots.
var versionDotRe = regexp.MustCompile(`(\d)\.(\d)`)

// Catalog is the normalized, provider-agnostic model metadata snapshot used by
// attractor runtime preflight and routing metadata checks.
type Catalog struct {
	Path   string
	SHA256 string
	Models map[string]ModelEntry

	// CoveredProviders is the set of normalized provider keys that have at
	// least one model entry in this catalog. Populated at load time so callers
	// can distinguish "model not found for a known provider" from "catalog has
	// no data for this provider at all."
	CoveredProviders map[string]bool
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

// CatalogCoversProvider returns true when the catalog was loaded from a source
// that includes at least one model for the given provider. A false return means
// the catalog cannot validate models for this provider — it has no opinion, not
// that the provider is invalid.
func CatalogCoversProvider(c *Catalog, provider string) bool {
	if c == nil || len(c.CoveredProviders) == 0 {
		return false
	}
	return c.CoveredProviders[modelmeta.NormalizeProvider(provider)]
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
	// Anthropic OpenRouter catalog uses dots in version numbers (claude-sonnet-4.5)
	// but the native API uses dashes (claude-sonnet-4-5). Normalize dots to dashes
	// on both sides so either format matches.
	if provider == "anthropic" {
		normQuery := versionDotRe.ReplaceAllString(inRelative, "${1}-${2}")
		for id, entry := range c.Models {
			ep := modelmeta.NormalizeProvider(entry.Provider)
			if ep == "" {
				ep = inferProviderFromModelID(id)
			}
			if ep != provider {
				continue
			}
			normEntry := versionDotRe.ReplaceAllString(providerRelativeModelID(provider, id), "${1}-${2}")
			if strings.EqualFold(normEntry, normQuery) {
				return true
			}
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
