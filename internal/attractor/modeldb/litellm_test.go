package modeldb

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadLiteLLMCatalog(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	_ = os.WriteFile(p, []byte(`{"data":[{"id":"openai/gpt-5","context_length":1000,"top_provider":{"max_completion_tokens":2000},"pricing":{"prompt":"0.000001"}}]}`), 0o644)
	c, err := LoadLiteLLMCatalog(p)
	if err != nil {
		t.Fatalf("LoadLiteLLMCatalog error: %v", err)
	}
	if c.SHA256 == "" {
		t.Fatalf("sha256 empty")
	}
	if _, ok := c.Models["openai/gpt-5"]; !ok {
		t.Fatalf("missing model entry")
	}
}

func TestLoadLiteLLMCatalog_LegacyPayloadFallback(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	_ = os.WriteFile(p, []byte(`{"gpt-5.2":{"litellm_provider":"openai","mode":"chat","max_input_tokens":1000,"max_output_tokens":2000,"input_cost_per_token":0.000001}}`), 0o644)
	c, err := LoadLiteLLMCatalog(p)
	if err != nil {
		t.Fatalf("LoadLiteLLMCatalog legacy fallback error: %v", err)
	}
	if _, ok := c.Models["gpt-5.2"]; !ok {
		t.Fatalf("missing legacy model entry")
	}
}
