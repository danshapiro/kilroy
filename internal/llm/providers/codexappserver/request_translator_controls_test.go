package codexappserver

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/llm"
)

func TestRequestTranslator_ApplyControlOverride_SupportedValues(t *testing.T) {
	controls := &transcriptControls{}
	warnings := []llm.Warning{}

	applyControlOverride("temperature", 0.7, controls, &warnings, "temperature")
	applyControlOverride("topP", 0.9, controls, &warnings, "topP")
	applyControlOverride("maxTokens", 42.0, controls, &warnings, "maxTokens")
	applyControlOverride("maxTokens", 64, controls, &warnings, "maxTokens")
	applyControlOverride("stopSequences", []any{"END", "STOP"}, controls, &warnings, "stopSequences")
	applyControlOverride("metadata", map[string]any{"team": "qa"}, controls, &warnings, "metadata")
	applyControlOverride("reasoningEffort", "medium", controls, &warnings, "reasoningEffort")

	if controls.Temperature == nil || *controls.Temperature != 0.7 {
		t.Fatalf("temperature: %#v", controls.Temperature)
	}
	if controls.TopP == nil || *controls.TopP != 0.9 {
		t.Fatalf("topP: %#v", controls.TopP)
	}
	if controls.MaxTokens == nil || *controls.MaxTokens != 64 {
		t.Fatalf("maxTokens: %#v", controls.MaxTokens)
	}
	if len(controls.StopSequences) != 2 || controls.StopSequences[0] != "END" || controls.StopSequences[1] != "STOP" {
		t.Fatalf("stopSequences: %#v", controls.StopSequences)
	}
	if controls.Metadata["team"] != "qa" {
		t.Fatalf("metadata: %#v", controls.Metadata)
	}
	if controls.ReasoningEff != "medium" {
		t.Fatalf("reasoningEffort: %q", controls.ReasoningEff)
	}
	if len(warnings) != 0 {
		t.Fatalf("did not expect warnings for supported values: %+v", warnings)
	}
}

func TestRequestTranslator_ApplyControlOverride_InvalidValuesEmitWarnings(t *testing.T) {
	controls := &transcriptControls{}
	warnings := []llm.Warning{}

	applyControlOverride("stopSequences", []any{"END", 2}, controls, &warnings, "stopSequences")
	applyControlOverride("reasoningEffort", "invalid-effort", controls, &warnings, "reasoningEffort")
	applyControlOverride("temperature", "not-a-number", controls, &warnings, "temperature")
	applyControlOverride("unknownKey", true, controls, &warnings, "unknownKey")

	if len(controls.StopSequences) != 0 {
		t.Fatalf("stopSequences should remain unset on invalid input: %#v", controls.StopSequences)
	}
	if controls.ReasoningEff != "" {
		t.Fatalf("reasoningEffort should remain empty for invalid value: %q", controls.ReasoningEff)
	}
	if controls.Temperature != nil {
		t.Fatalf("temperature should remain nil on invalid input: %#v", controls.Temperature)
	}
	if len(warnings) < 4 {
		t.Fatalf("expected warnings for invalid values, got %d (%+v)", len(warnings), warnings)
	}
}

func TestRequestTranslator_ApplyProviderOptions_MapsKnownKeysAndWarnsUnknown(t *testing.T) {
	params := map[string]any{}
	controls := &transcriptControls{}
	warnings := []llm.Warning{}

	applyProviderOptions(llm.Request{
		ProviderOptions: map[string]any{
			"codex_app_server": map[string]any{
				"cwd":              "/tmp/project",
				"approval_policy":  "never",
				"temperature":      0.2,
				"reasoning_effort": "high",
				"unsupportedX":     true,
			},
		},
	}, params, controls, &warnings)

	if params["cwd"] != "/tmp/project" {
		t.Fatalf("cwd mapping: %#v", params["cwd"])
	}
	if params["approvalPolicy"] != "never" {
		t.Fatalf("approvalPolicy mapping: %#v", params["approvalPolicy"])
	}
	if controls.Temperature == nil || *controls.Temperature != 0.2 {
		t.Fatalf("temperature override: %#v", controls.Temperature)
	}
	if controls.ReasoningEff != "high" {
		t.Fatalf("reasoningEffort override: %q", controls.ReasoningEff)
	}
	if len(warnings) == 0 {
		t.Fatalf("expected warning for unsupported provider option")
	}
}

func TestRequestTranslator_WarningHelpers(t *testing.T) {
	w := warningForUnsupportedProviderOption("x_opt")
	if w.Code != "unsupported_option" {
		t.Fatalf("warning code: %q", w.Code)
	}
	if out := outWarning(&[]llm.Warning{}); out == nil {
		t.Fatalf("outWarning should return same pointer")
	}
}
