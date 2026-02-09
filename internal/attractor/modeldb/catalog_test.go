package modeldb

import "testing"

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
