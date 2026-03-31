package engine

import "testing"

func TestIsCLIOnlyModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gpt-5.4-spark", true},
		{"GPT-5.4-SPARK", true},  // case-insensitive
		{"openai/gpt-5.4-spark", true}, // with provider prefix
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
