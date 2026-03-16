package llm

import (
	"fmt"
	"strings"
)

// Tool names must match the strict common subset across providers:
// [a-zA-Z][a-zA-Z0-9_]* with a max length of 64.
func ValidateToolName(name string) error {
	n := strings.TrimSpace(name)
	if n == "" {
		return &ConfigurationError{Message: "tool name is required"}
	}
	if len(n) > 64 {
		return &ConfigurationError{Message: fmt.Sprintf("tool name too long: %d > 64", len(n))}
	}
	b0 := n[0]
	if !((b0 >= 'a' && b0 <= 'z') || (b0 >= 'A' && b0 <= 'Z')) {
		return &ConfigurationError{Message: fmt.Sprintf("invalid tool name %q: must start with a letter", n)}
	}
	for i := 1; i < len(n); i++ {
		b := n[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_' {
			continue
		}
		return &ConfigurationError{Message: fmt.Sprintf("invalid tool name %q: invalid character %q", n, string(b))}
	}
	return nil
}

func defaultToolParameters() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}
}

func validateToolParameters(params map[string]any) error {
	if params == nil {
		// Treat nil as an empty object schema (common "no-args tool" case).
		return nil
	}
	typAny, ok := params["type"]
	if !ok {
		return &ConfigurationError{Message: "tool parameters must include type=object at the schema root"}
	}
	typ, ok := typAny.(string)
	if !ok || strings.ToLower(strings.TrimSpace(typ)) != "object" {
		return &ConfigurationError{Message: fmt.Sprintf("tool parameters must have type=object at the schema root; got %#v", typAny)}
	}
	return nil
}
