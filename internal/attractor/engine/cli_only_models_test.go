package engine

import "testing"

func TestIsCLIOnlyModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gpt-5.4-spark", false},
		{"GPT-5.4-SPARK", false},       // case-insensitive
		{"openai/gpt-5.4-spark", false}, // with provider prefix
		{"gpt-5.4", false},             // regular codex
		{"gpt-5.4", false},
		{"claude-opus-4-6", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isCLIOnlyModel(tt.model); got != tt.want {
			t.Errorf("isCLIOnlyModel(%q) = %v, want %v", tt.model, got, tt.want)
		}
	}
}

func TestIsCLIOnlyModel_UsesConfiguredRegistry(t *testing.T) {
	orig := cliOnlyModelIDs
	cliOnlyModelIDs = map[string]bool{
		"test-cli-only-model": true,
	}
	t.Cleanup(func() {
		cliOnlyModelIDs = orig
	})

	if got := isCLIOnlyModel("test-cli-only-model"); !got {
		t.Fatalf("isCLIOnlyModel(test-cli-only-model) = %v, want true", got)
	}
	if got := isCLIOnlyModel("openai/test-cli-only-model"); !got {
		t.Fatalf("isCLIOnlyModel(openai/test-cli-only-model) = %v, want true", got)
	}
}
