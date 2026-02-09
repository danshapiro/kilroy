package modeldb

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// LiteLLMCatalog is a deprecated compatibility alias for legacy call sites that
// still use LiteLLM naming.
//
// Deprecated: use Catalog.
type LiteLLMCatalog struct {
	Path   string
	SHA256 string
	Models map[string]LiteLLMModelEntry
}

// LiteLLMModelEntry is a deprecated compatibility shape used by legacy call
// sites. New code should use ModelEntry.
//
// Deprecated: use ModelEntry.
type LiteLLMModelEntry struct {
	LiteLLMProvider string `json:"litellm_provider"`
	Mode            string `json:"mode"`

	MaxInputTokens  any `json:"max_input_tokens"`  // may be number or string in upstream
	MaxOutputTokens any `json:"max_output_tokens"` // may be number or string in upstream
	MaxTokens       any `json:"max_tokens"`        // legacy

	InputCostPerToken           *float64 `json:"input_cost_per_token"`
	OutputCostPerToken          *float64 `json:"output_cost_per_token"`
	OutputCostPerReasoningToken *float64 `json:"output_cost_per_reasoning_token"`

	DeprecationDate string `json:"deprecation_date"`
}

// LoadLiteLLMCatalog is a deprecated compatibility wrapper.
//
// Deprecated: use LoadCatalogFromOpenRouterJSON.
// TODO(kilroy): remove LiteLLM wrappers after 2026-06-30.
func LoadLiteLLMCatalog(path string) (*LiteLLMCatalog, error) {
	// Prefer OpenRouter model info payloads.
	if cat, err := LoadCatalogFromOpenRouterJSON(path); err == nil {
		return catalogToLiteLLMCompat(cat), nil
	}
	// Temporary fallback for older pinned fixtures that still use the historical
	// LiteLLM object-map payload format.
	return LoadLegacyLiteLLMCatalog(path)
}

func catalogToLiteLLMCompat(cat *Catalog) *LiteLLMCatalog {
	if cat == nil {
		return nil
	}
	out := &LiteLLMCatalog{
		Path:   cat.Path,
		SHA256: cat.SHA256,
		Models: make(map[string]LiteLLMModelEntry, len(cat.Models)),
	}
	for id, m := range cat.Models {
		entry := LiteLLMModelEntry{
			LiteLLMProvider:    m.Provider,
			Mode:               m.Mode,
			MaxInputTokens:     m.ContextWindow,
			InputCostPerToken:  m.InputCostPerToken,
			OutputCostPerToken: m.OutputCostPerToken,
		}
		if m.MaxOutputTokens != nil {
			entry.MaxOutputTokens = *m.MaxOutputTokens
		}
		out.Models[id] = entry
	}
	return out
}

// LoadLegacyLiteLLMCatalog loads the historical LiteLLM object-map payload
// shape directly.
//
// Deprecated: temporary compatibility shim for legacy pinned fixtures.
func LoadLegacyLiteLLMCatalog(path string) (*LiteLLMCatalog, error) {
	return loadLegacyLiteLLMCatalog(path)
}

func loadLegacyLiteLLMCatalog(path string) (*LiteLLMCatalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	sha := hex.EncodeToString(sum[:])

	var models map[string]LiteLLMModelEntry
	if err := json.Unmarshal(b, &models); err != nil {
		return nil, err
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("litellm catalog is empty: %s", path)
	}
	return &LiteLLMCatalog{
		Path:   path,
		SHA256: sha,
		Models: models,
	}, nil
}
