package llmclient

import "testing"

func TestNewFromEnv_ErrorsWhenNoProvidersConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	_, err := NewFromEnv()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
