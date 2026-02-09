package llm

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadModelCatalogFromOpenRouterJSON_GetListLatest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	body := `{
  "data": [
    {
      "id": "openai/gpt-5",
      "context_length": 272000,
      "pricing": {"prompt":"0.000001","completion":"0.00001"},
      "supported_parameters": ["tools", "reasoning"],
      "architecture": {"input_modalities":["text"],"output_modalities":["text"]}
    }
  ]
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadModelCatalogFromOpenRouterJSON(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.GetModelInfo("openai/gpt-5") == nil {
		t.Fatalf("expected model")
	}
}

func TestLoadModelCatalogFromLiteLLMJSON_CompatibilityWrapper_GetListLatest(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	body := `{
  "data": [
    {
      "id": "openai/gpt-5",
      "context_length": 272000,
      "supported_parameters": ["tools","reasoning"],
      "architecture": {"input_modalities":["text","image"],"output_modalities":["text"]},
      "pricing": {"prompt":"0.000001","completion":"0.000002"},
      "top_provider": {"max_completion_tokens": 128000}
    },
    {
      "id": "openai/gpt-5-mini",
      "context_length": 128000,
      "supported_parameters": ["tools"],
      "architecture": {"input_modalities":["text"],"output_modalities":["text"]},
      "pricing": {"prompt":"0.0000002","completion":"0.0000008"},
      "top_provider": {"max_completion_tokens": 32000}
    },
    {
      "id": "anthropic/claude-opus-4-6",
      "context_length": 200000,
      "supported_parameters": ["tools","reasoning"],
      "architecture": {"input_modalities":["text"],"output_modalities":["text"]},
      "pricing": {"prompt":"0.000015","completion":"0.000075"},
      "top_provider": {"max_completion_tokens": 8192}
    },
    {
      "id": "google/gemini-3-flash-preview",
      "context_length": 1000000,
      "supported_parameters": ["tools"],
      "architecture": {"input_modalities":["text","image"],"output_modalities":["text"]},
      "pricing": {"prompt":"0.0000003","completion":"0.0000012"},
      "top_provider": {"max_completion_tokens": 8192}
    }
  ]
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	c, err := LoadModelCatalogFromLiteLLMJSON(p)
	if err != nil {
		t.Fatalf("LoadModelCatalogFromLiteLLMJSON: %v", err)
	}
	if got, wantMin := len(c.Models), 4; got != wantMin {
		t.Fatalf("models: got %d want %d", got, wantMin)
	}

	mi := c.GetModelInfo("openai/gpt-5")
	if mi == nil {
		t.Fatalf("GetModelInfo returned nil")
	}
	if mi.Provider != "openai" {
		t.Fatalf("provider: got %q want %q", mi.Provider, "openai")
	}
	if mi.ContextWindow != 272000 {
		t.Fatalf("context_window: got %d want %d", mi.ContextWindow, 272000)
	}
	if mi.MaxOutputTokens == nil || *mi.MaxOutputTokens != 128000 {
		t.Fatalf("max_output_tokens: got %v want %d", mi.MaxOutputTokens, 128000)
	}
	if mi.InputCostPerMillion == nil || *mi.InputCostPerMillion != 1.0 {
		t.Fatalf("input_cost_per_million: got %v want %v", mi.InputCostPerMillion, 1.0)
	}
	if mi.OutputCostPerMillion == nil || *mi.OutputCostPerMillion != 2.0 {
		t.Fatalf("output_cost_per_million: got %v want %v", mi.OutputCostPerMillion, 2.0)
	}

	opens := c.ListModels("openai")
	if got, want := len(opens), 2; got != want {
		t.Fatalf("openai models: got %d want %d", got, want)
	}
	gems := c.ListModels("gemini") // alias => google
	if got, want := len(gems), 1; got != want {
		t.Fatalf("gemini/google models: got %d want %d", got, want)
	}
	if gems[0].Provider != "google" {
		t.Fatalf("gemini provider normalized: got %q want %q", gems[0].Provider, "google")
	}

	latestOpenAI := c.GetLatestModel("openai", "")
	if latestOpenAI == nil || latestOpenAI.ID != "openai/gpt-5" {
		t.Fatalf("latest openai: got %+v want openai/gpt-5", latestOpenAI)
	}
	latestVision := c.GetLatestModel("openai", "vision")
	if latestVision == nil || latestVision.ID != "openai/gpt-5" {
		t.Fatalf("latest openai vision: got %+v want openai/gpt-5", latestVision)
	}
	latestReasoning := c.GetLatestModel("google", "reasoning")
	if latestReasoning != nil {
		t.Fatalf("expected no google reasoning model in sample catalog; got %+v", latestReasoning)
	}
}

func TestLoadModelCatalogFromLiteLLMJSON_CompatibilityWrapper_AcceptsLegacyFormat(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "catalog.json")
	body := `{
  "sample_spec": {"litellm_provider":"openai","mode":"chat"},
  "gpt-5.2": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":1000,"max_output_tokens":2000,
    "input_cost_per_token":0.000001,"output_cost_per_token":0.000002,
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gpt-5.2-mini": {
    "litellm_provider":"openai","mode":"chat",
    "max_input_tokens":500,"max_output_tokens":1000,
    "supports_function_calling":true
  },
  "claude-opus-4-6": {
    "litellm_provider":"anthropic","mode":"chat",
    "max_input_tokens":"200000","max_output_tokens":"8192",
    "supports_function_calling":true,"supports_vision":true,"supports_reasoning":true
  },
  "gemini-3-flash-preview": {
    "litellm_provider":"gemini","mode":"chat",
    "max_input_tokens":1000000,"max_output_tokens":8192,
    "supports_function_calling":true,"supports_vision":true
  },
  "text-embedding-3-large": {
    "litellm_provider":"openai","mode":"embedding","max_input_tokens":8191
  }
}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadModelCatalogFromLiteLLMJSON(p)
	if err != nil {
		t.Fatalf("LoadModelCatalogFromLiteLLMJSON: %v", err)
	}
	if got, want := len(c.Models), 4; got != want {
		t.Fatalf("models: got %d want %d", got, want)
	}
	if c.GetModelInfo("gpt-5.2") == nil {
		t.Fatalf("expected gpt-5.2 model")
	}
}
