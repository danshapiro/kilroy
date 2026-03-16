package llm

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateToolName(t *testing.T) {
	for _, name := range []string{"t", "Tool_1", "a_b_c", "Z9"} {
		if err := ValidateToolName(name); err != nil {
			t.Fatalf("ValidateToolName(%q): %v", name, err)
		}
	}

	for _, name := range []string{"", "  ", "1bad", "_bad", "bad-name", "bad.name"} {
		if err := ValidateToolName(name); err == nil {
			t.Fatalf("expected error for name=%q", name)
		}
	}
	long := "a" + strings.Repeat("b", 64) // 65 chars
	if err := ValidateToolName(long); err == nil {
		t.Fatalf("expected error for long name (%d chars)", len(long))
	}
}

func TestRequestValidate_ToolsValidated(t *testing.T) {
	req := Request{Model: "m", Messages: []Message{User("hi")}}
	if err := req.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Invalid tool name.
	req.Tools = []ToolDefinition{{Name: "1bad", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}
	if err := req.Validate(); err == nil {
		t.Fatalf("expected error for invalid tool name")
	} else {
		var ce *ConfigurationError
		if !errors.As(err, &ce) {
			t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
		}
	}

	// Invalid schema root type.
	req.Tools = []ToolDefinition{{Name: "ok", Parameters: map[string]any{"type": "string"}}}
	if err := req.Validate(); err == nil {
		t.Fatalf("expected error for invalid schema root type")
	}

	// Nil schema treated as empty object schema.
	req.Tools = []ToolDefinition{{Name: "ok", Parameters: nil}}
	if err := req.Validate(); err != nil {
		t.Fatalf("expected nil schema to be allowed; got %v", err)
	}
}
