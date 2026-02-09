package modeldb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCatalogFromOpenRouterJSON_ParsesPricingAndCapabilities(t *testing.T) {
	p := writeTempFile(t, `{
	  "data": [{
	    "id": "openai/gpt-5",
	    "context_length": 272000,
	    "supported_parameters": ["tools", "reasoning"],
	    "architecture": {"input_modalities":["text","image"],"output_modalities":["text"]},
	    "pricing": {"prompt":"0.00000125", "completion":"0.00001"},
	    "top_provider": {"max_completion_tokens": 128000}
	  }]
	}`)
	c, err := LoadCatalogFromOpenRouterJSON(p)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	m := c.Models["openai/gpt-5"]
	if m.Provider != "openai" || !m.SupportsTools || !m.SupportsReasoning || !m.SupportsVision {
		t.Fatalf("unexpected parsed model: %+v", m)
	}
	if m.ContextWindow != 272000 {
		t.Fatalf("context window: got %d want 272000", m.ContextWindow)
	}
	if m.MaxOutputTokens == nil || *m.MaxOutputTokens != 128000 {
		t.Fatalf("max output tokens: got %#v want 128000", m.MaxOutputTokens)
	}
	if m.InputCostPerToken == nil || *m.InputCostPerToken != 0.00000125 {
		t.Fatalf("input cost: got %#v", m.InputCostPerToken)
	}
	if m.OutputCostPerToken == nil || *m.OutputCostPerToken != 0.00001 {
		t.Fatalf("output cost: got %#v", m.OutputCostPerToken)
	}
}

func writeTempFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}
