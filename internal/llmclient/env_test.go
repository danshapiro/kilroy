package llmclient

import "testing"

func TestNewFromEnv_ErrorsWhenNoProvidersConfigured(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CODEX_APP_SERVER_COMMAND", "")
	t.Setenv("CODEX_APP_SERVER_ARGS", "")
	t.Setenv("CODEX_APP_SERVER_COMMAND_ARGS", "")
	t.Setenv("CODEX_APP_SERVER_AUTO_DISCOVER", "")
	_, err := NewFromEnv()
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}
func TestNewFromEnv_RegistersCodexAppServerWhenCommandOverrideIsSet(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GEMINI_API_KEY", "")
	t.Setenv("GOOGLE_API_KEY", "")
	t.Setenv("CODEX_APP_SERVER_COMMAND", "codex")
	t.Setenv("CODEX_APP_SERVER_ARGS", "")
	t.Setenv("CODEX_APP_SERVER_COMMAND_ARGS", "")
	t.Setenv("CODEX_APP_SERVER_AUTO_DISCOVER", "")
	c, err := NewFromEnv()
	if err != nil {
		t.Fatalf("NewFromEnv: %v", err)
	}
	names := c.ProviderNames()
	if len(names) != 1 || names[0] != "codex-app-server" {
		t.Fatalf("provider names: got %v want [codex-app-server]", names)
	}
}
