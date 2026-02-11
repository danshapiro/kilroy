package modeldb

import (
	"os"
	"testing"
)

func TestCatalogHasProviderModel_AcceptsCanonicalAndProviderRelativeIDs(t *testing.T) {
	c := &Catalog{Models: map[string]ModelEntry{
		"openai/gpt-5":       {Provider: "openai"},
		"anthropic/claude-4": {Provider: "anthropic"},
	}}
	if !CatalogHasProviderModel(c, "openai", "gpt-5") {
		t.Fatalf("expected provider-relative openai model id to resolve")
	}
	if !CatalogHasProviderModel(c, "openai", "openai/gpt-5") {
		t.Fatalf("expected canonical openai model id to resolve")
	}
}

func TestCatalogCoversProvider_TrueForCoveredProvider(t *testing.T) {
	c := &Catalog{CoveredProviders: map[string]bool{"openai": true, "anthropic": true}}
	if !CatalogCoversProvider(c, "openai") {
		t.Fatalf("expected CatalogCoversProvider=true for openai")
	}
	if !CatalogCoversProvider(c, "anthropic") {
		t.Fatalf("expected CatalogCoversProvider=true for anthropic")
	}
}

func TestCatalogCoversProvider_FalseForUncoveredProvider(t *testing.T) {
	c := &Catalog{CoveredProviders: map[string]bool{"openai": true}}
	if CatalogCoversProvider(c, "cerebras") {
		t.Fatalf("expected CatalogCoversProvider=false for cerebras (not in catalog)")
	}
}

func TestCatalogCoversProvider_ResolvesAliases(t *testing.T) {
	c := &Catalog{CoveredProviders: map[string]bool{"kimi": true, "zai": true}}
	if !CatalogCoversProvider(c, "moonshot") {
		t.Fatalf("expected CatalogCoversProvider to resolve moonshot alias to kimi")
	}
	if CatalogCoversProvider(c, "cerebras-ai") {
		t.Fatalf("expected CatalogCoversProvider=false for cerebras-ai (cerebras not covered)")
	}
}

func TestLoadCatalogFromOpenRouterJSON_PopulatesCoveredProviders(t *testing.T) {
	path := t.TempDir() + "/catalog.json"
	data := `{"data":[{"id":"openai/gpt-5"},{"id":"z-ai/glm-4.7"}]}`
	if err := writeTestFile(t, path, data); err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalogFromOpenRouterJSON(path)
	if err != nil {
		t.Fatalf("LoadCatalogFromOpenRouterJSON: %v", err)
	}
	if !c.CoveredProviders["openai"] {
		t.Fatalf("expected CoveredProviders to include openai")
	}
	if !c.CoveredProviders["zai"] {
		t.Fatalf("expected CoveredProviders to include zai (from z-ai prefix)")
	}
	if c.CoveredProviders["cerebras"] {
		t.Fatalf("expected CoveredProviders to NOT include cerebras")
	}
}

func writeTestFile(t *testing.T, path string, content string) error {
	t.Helper()
	return os.WriteFile(path, []byte(content), 0o644)
}

func TestCatalogHasProviderModel_MatchesAnthropicDotsAndDashes(t *testing.T) {
	c := &Catalog{Models: map[string]ModelEntry{
		"anthropic/claude-sonnet-4.5": {Provider: "anthropic"},
		"anthropic/claude-opus-4.6":   {Provider: "anthropic"},
		"anthropic/claude-3.7-sonnet": {Provider: "anthropic"},
	}}
	// Native API format (dashes) should match catalog entries (dots).
	if !CatalogHasProviderModel(c, "anthropic", "claude-sonnet-4-5") {
		t.Fatalf("expected dash-format model to match dot-format catalog entry")
	}
	if !CatalogHasProviderModel(c, "anthropic", "claude-opus-4-6") {
		t.Fatalf("expected dash-format opus to match dot-format catalog entry")
	}
	if !CatalogHasProviderModel(c, "anthropic", "claude-3-7-sonnet") {
		t.Fatalf("expected dash-format 3-7 to match dot-format catalog entry")
	}
	// Dot format should still match directly.
	if !CatalogHasProviderModel(c, "anthropic", "claude-sonnet-4.5") {
		t.Fatalf("expected dot-format model to still match")
	}
}

func TestCatalogHasProviderModel_AcceptsOpenRouterProviderPrefixes(t *testing.T) {
	c := &Catalog{Models: map[string]ModelEntry{
		"moonshotai/kimi-k2.5": {},
		"z-ai/glm-4.7":         {},
	}}
	if !CatalogHasProviderModel(c, "kimi", "kimi-k2.5") {
		t.Fatalf("expected kimi provider-relative model to match moonshotai prefix")
	}
	if !CatalogHasProviderModel(c, "kimi", "moonshotai/kimi-k2.5") {
		t.Fatalf("expected kimi canonical/openrouter id to match")
	}
	if !CatalogHasProviderModel(c, "zai", "glm-4.7") {
		t.Fatalf("expected zai provider-relative model to match z-ai prefix")
	}
	if !CatalogHasProviderModel(c, "zai", "z-ai/glm-4.7") {
		t.Fatalf("expected zai canonical/openrouter id to match")
	}
}
