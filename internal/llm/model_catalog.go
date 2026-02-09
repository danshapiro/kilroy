package llm

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/strongdm/kilroy/internal/modelmeta"
)

// ModelInfo is the normalized model metadata entry, primarily sourced from OpenRouter model info
// in Kilroy. This is metadata-only and is not used as a provider call path.
type ModelInfo struct {
	ID                   string   `json:"id"`
	Provider             string   `json:"provider"`
	DisplayName          string   `json:"display_name"`
	ContextWindow        int      `json:"context_window"`
	MaxOutputTokens      *int     `json:"max_output_tokens,omitempty"`
	SupportsTools        bool     `json:"supports_tools"`
	SupportsVision       bool     `json:"supports_vision"`
	SupportsReasoning    bool     `json:"supports_reasoning"`
	InputCostPerMillion  *float64 `json:"input_cost_per_million,omitempty"`
	OutputCostPerMillion *float64 `json:"output_cost_per_million,omitempty"`
	Aliases              []string `json:"aliases,omitempty"`
}

type ModelCatalog struct {
	Models []ModelInfo
	byID   map[string]ModelInfo
}

func (c *ModelCatalog) GetModelInfo(modelID string) *ModelInfo {
	if c == nil {
		return nil
	}
	if c.byID == nil {
		c.buildIndex()
	}
	if mi, ok := c.byID[strings.TrimSpace(modelID)]; ok {
		out := mi
		return &out
	}
	return nil
}

func (c *ModelCatalog) ListModels(provider string) []ModelInfo {
	if c == nil {
		return nil
	}
	p := strings.ToLower(strings.TrimSpace(provider))
	if p == "gemini" {
		p = "google"
	}
	if p == "" {
		return append([]ModelInfo{}, c.Models...)
	}
	var out []ModelInfo
	for _, m := range c.Models {
		if strings.ToLower(m.Provider) == p {
			out = append(out, m)
		}
	}
	return out
}

func (c *ModelCatalog) GetLatestModel(provider string, capability string) *ModelInfo {
	models := c.ListModels(provider)
	capability = strings.ToLower(strings.TrimSpace(capability))

	filtered := models[:0]
	for _, m := range models {
		switch capability {
		case "":
			filtered = append(filtered, m)
		case "tools":
			if m.SupportsTools {
				filtered = append(filtered, m)
			}
		case "vision":
			if m.SupportsVision {
				filtered = append(filtered, m)
			}
		case "reasoning":
			if m.SupportsReasoning {
				filtered = append(filtered, m)
			}
		default:
			// Unknown capability filter => no results.
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	sort.Slice(filtered, func(i, j int) bool {
		if filtered[i].ContextWindow != filtered[j].ContextWindow {
			return filtered[i].ContextWindow > filtered[j].ContextWindow
		}
		// Stable tie-break: lexical ID descending.
		return filtered[i].ID > filtered[j].ID
	})
	out := filtered[0]
	return &out
}

func (c *ModelCatalog) buildIndex() {
	by := make(map[string]ModelInfo, len(c.Models))
	for _, m := range c.Models {
		if _, exists := by[m.ID]; exists {
			// Leave the first entry to avoid silently changing behavior on duplicates.
			continue
		}
		by[m.ID] = m
	}
	c.byID = by
}

type openRouterCatalogPayload struct {
	Data []openRouterCatalogModel `json:"data"`
}

type openRouterCatalogModel struct {
	ID                  string   `json:"id"`
	ContextLength       int      `json:"context_length"`
	SupportedParameters []string `json:"supported_parameters"`
	Architecture        struct {
		InputModalities  []string `json:"input_modalities"`
		OutputModalities []string `json:"output_modalities"`
	} `json:"architecture"`
	Pricing struct {
		Prompt     string `json:"prompt"`
		Completion string `json:"completion"`
	} `json:"pricing"`
	TopProvider struct {
		ContextLength       int `json:"context_length"`
		MaxCompletionTokens int `json:"max_completion_tokens"`
	} `json:"top_provider"`
}

// LoadModelCatalogFromOpenRouterJSON loads model metadata from OpenRouter's
// /api/v1/models payload shape: {"data":[...]}.
func LoadModelCatalogFromOpenRouterJSON(path string) (*ModelCatalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var payload openRouterCatalogPayload
	if err := json.Unmarshal(b, &payload); err != nil {
		return nil, err
	}
	if len(payload.Data) == 0 {
		return nil, fmt.Errorf("openrouter catalog is empty: %s", path)
	}

	var models []ModelInfo
	for _, v := range payload.Data {
		id := strings.TrimSpace(v.ID)
		if id == "" {
			continue
		}
		prov := modelmeta.ProviderFromModelID(id)
		ctxWindow := v.ContextLength
		if ctxWindow == 0 {
			ctxWindow = v.TopProvider.ContextLength
		}
		maxOut := v.TopProvider.MaxCompletionTokens
		var maxOutPtr *int
		if maxOut > 0 {
			maxOutPtr = &maxOut
		}

		inCost := modelmeta.ParseFloatStringPtr(v.Pricing.Prompt)
		outCost := modelmeta.ParseFloatStringPtr(v.Pricing.Completion)
		inPerM := scalePerMillion(inCost)
		outPerM := scalePerMillion(outCost)

		models = append(models, ModelInfo{
			ID:                   id,
			Provider:             prov,
			DisplayName:          id,
			ContextWindow:        ctxWindow,
			MaxOutputTokens:      maxOutPtr,
			SupportsTools:        modelmeta.ContainsFold(v.SupportedParameters, "tools"),
			SupportsVision:       modelmeta.ContainsFold(v.Architecture.InputModalities, "image") || modelmeta.ContainsFold(v.Architecture.OutputModalities, "image"),
			SupportsReasoning:    modelmeta.ContainsFold(v.SupportedParameters, "reasoning") || modelmeta.ContainsFold(v.SupportedParameters, "include_reasoning"),
			InputCostPerMillion:  inPerM,
			OutputCostPerMillion: outPerM,
			Aliases:              nil,
		})
	}

	// Stable ordering.
	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].ID < models[j].ID
	})
	return &ModelCatalog{Models: models}, nil
}

// Deprecated: use LoadModelCatalogFromOpenRouterJSON.
func LoadModelCatalogFromLiteLLMJSON(path string) (*ModelCatalog, error) {
	cat, err := LoadModelCatalogFromOpenRouterJSON(path)
	if err == nil {
		return cat, nil
	}
	legacy, legacyErr := loadModelCatalogFromLiteLLMJSONLegacy(path)
	if legacyErr != nil {
		return nil, fmt.Errorf("load model catalog %q failed (openrouter=%v, litellm=%v)", path, err, legacyErr)
	}
	return legacy, nil
}

func loadModelCatalogFromLiteLLMJSONLegacy(path string) (*ModelCatalog, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var raw map[string]map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("litellm catalog is empty: %s", path)
	}

	var models []ModelInfo
	for id, v := range raw {
		if id == "sample_spec" {
			continue
		}
		mode, _ := v["mode"].(string)
		if strings.TrimSpace(mode) != "" && strings.TrimSpace(mode) != "chat" {
			continue
		}

		prov := modelmeta.NormalizeProvider(fmt.Sprint(v["litellm_provider"]))
		ctxWindow := parseIntAny(v["max_input_tokens"])
		if ctxWindow == 0 {
			ctxWindow = parseIntAny(v["max_tokens"])
		}
		maxOut := parseIntAny(v["max_output_tokens"])
		if maxOut == 0 {
			maxOut = parseIntAny(v["max_tokens"])
		}
		var maxOutPtr *int
		if maxOut > 0 {
			maxOutPtr = &maxOut
		}

		inCost := parseFloatAny(v["input_cost_per_token"])
		outCost := parseFloatAny(v["output_cost_per_token"])
		inPerM := scalePerMillion(inCost)
		outPerM := scalePerMillion(outCost)

		models = append(models, ModelInfo{
			ID:                   id,
			Provider:             prov,
			DisplayName:          id,
			ContextWindow:        ctxWindow,
			MaxOutputTokens:      maxOutPtr,
			SupportsTools:        parseBoolAny(v["supports_function_calling"]),
			SupportsVision:       parseBoolAny(v["supports_vision"]),
			SupportsReasoning:    parseBoolAny(v["supports_reasoning"]),
			InputCostPerMillion:  inPerM,
			OutputCostPerMillion: outPerM,
			Aliases:              nil,
		})
	}

	sort.Slice(models, func(i, j int) bool {
		if models[i].Provider != models[j].Provider {
			return models[i].Provider < models[j].Provider
		}
		return models[i].ID < models[j].ID
	})
	return &ModelCatalog{Models: models}, nil
}

func parseIntAny(v any) int {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case float64:
		return int(x)
	case int:
		return x
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	default:
		return 0
	}
}

func parseBoolAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(x))
		return b
	default:
		return false
	}
}

func parseFloatAny(v any) *float64 {
	switch x := v.(type) {
	case json.Number:
		f, err := x.Float64()
		if err != nil {
			return nil
		}
		return &f
	case float64:
		return &x
	case int:
		f := float64(x)
		return &f
	case string:
		return modelmeta.ParseFloatStringPtr(x)
	default:
		return nil
	}
}

func scalePerMillion(perToken *float64) *float64 {
	if perToken == nil {
		return nil
	}
	v := *perToken * 1_000_000
	return &v
}
