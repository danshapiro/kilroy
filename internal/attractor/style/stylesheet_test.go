package style

import (
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

func TestStylesheet_ParseAndApply(t *testing.T) {
	ss := `
* { llm_model: claude-sonnet-4-5; llm_provider: anthropic; }
box { reasoning_effort: low; }
.code { llm_model: claude-opus-4-6; }
#n1 { llm_provider: openai; reasoning_effort: high; }
`
	rules, err := ParseStylesheet(ss)
	if err != nil {
		t.Fatalf("ParseStylesheet error: %v", err)
	}
	g := model.NewGraph("G")
	n1 := model.NewNode("n1")
	n1.Attrs["shape"] = "box"
	n1.Attrs["class"] = "code"
	n2 := model.NewNode("n2")
	n2.Attrs["shape"] = "diamond"
	n2.Attrs["llm_model"] = "explicit-model"
	if err := g.AddNode(n1); err != nil {
		t.Fatalf("AddNode n1: %v", err)
	}
	if err := g.AddNode(n2); err != nil {
		t.Fatalf("AddNode n2: %v", err)
	}

	if err := ApplyStylesheet(g, rules); err != nil {
		t.Fatalf("ApplyStylesheet error: %v", err)
	}

	if got := g.Nodes["n1"].Attrs["llm_model"]; got != "claude-opus-4-6" {
		t.Fatalf("n1 llm_model: got %q", got)
	}
	if got := g.Nodes["n1"].Attrs["llm_provider"]; got != "openai" {
		t.Fatalf("n1 llm_provider: got %q", got)
	}
	if got := g.Nodes["n1"].Attrs["reasoning_effort"]; got != "high" {
		t.Fatalf("n1 reasoning_effort: got %q", got)
	}

	if got := g.Nodes["n2"].Attrs["llm_model"]; got != "explicit-model" {
		t.Fatalf("n2 llm_model should not be overridden: got %q", got)
	}
	if got := g.Nodes["n2"].Attrs["llm_provider"]; got != "anthropic" {
		t.Fatalf("n2 llm_provider: got %q", got)
	}
}

func TestParseStylesheet_RejectsUnicodeClassName(t *testing.T) {
	// Spec §8.2: ClassName ::= [a-z0-9-]+ — lowercase ASCII only.
	cases := []struct {
		name  string
		input string
	}{
		{"uppercase", `.MyClass { llm_model: test; }`},
		{"accented", `.café { llm_model: test; }`},
		{"CJK", `.中文 { llm_model: test; }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStylesheet(tc.input)
			if err == nil {
				t.Fatalf("expected parse error for class name %q, got nil", tc.input)
			}
		})
	}
}

func TestParseStylesheet_AcceptsValidClassNames(t *testing.T) {
	// Spec §8.2: ClassName ::= [a-z0-9-]+
	cases := []string{
		`.code { llm_model: test; }`,
		`.my-class { llm_model: test; }`,
		`.class-123 { llm_model: test; }`,
		`.a { llm_model: test; }`,
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			rules, err := ParseStylesheet(input)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(rules) != 1 {
				t.Fatalf("expected 1 rule, got %d", len(rules))
			}
			if rules[0].Kind != SelectorClass {
				t.Fatalf("expected SelectorClass, got %d", rules[0].Kind)
			}
		})
	}
}

func TestParseStylesheet_RejectsUnicodeInIdentifier(t *testing.T) {
	// Spec §2.2: Identifier ::= [A-Za-z_][A-Za-z0-9_]* — ASCII only.
	// The stylesheet's #id selector uses parseIdent().
	cases := []struct {
		name  string
		input string
	}{
		{"accented id", `#café { llm_model: test; }`},
		{"CJK id", `#中文 { llm_model: test; }`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseStylesheet(tc.input)
			if err == nil {
				t.Fatalf("expected parse error for %q, got nil", tc.input)
			}
		})
	}
}

func TestParseStylesheet_RejectsUnicodeInShapeSelector(t *testing.T) {
	// parseIdentLike should reject non-ASCII.
	_, err := ParseStylesheet(`böx { llm_model: test; }`)
	if err == nil {
		t.Fatalf("expected parse error for Unicode shape selector, got nil")
	}
}

func TestParseStylesheet_AcceptsASCIIIdentifiers(t *testing.T) {
	// ASCII identifiers and selectors must still work.
	ss := `
* { llm_model: default-model; }
box { reasoning_effort: low; }
.my-class { llm_model: class-model; }
#my_node_1 { llm_provider: anthropic; }
`
	rules, err := ParseStylesheet(ss)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rules) != 4 {
		t.Fatalf("expected 4 rules, got %d", len(rules))
	}
}

func TestIsIdentStart_ASCIIOnly(t *testing.T) {
	// ASCII letters and underscore accepted.
	for _, r := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_" {
		if !isIdentStart(r) {
			t.Errorf("isIdentStart(%q) = false, want true", r)
		}
	}
	// Non-ASCII rejected.
	for _, r := range []rune{'ä', 'é', 'Ω', '中'} {
		if isIdentStart(r) {
			t.Errorf("isIdentStart(%q) = true, want false", r)
		}
	}
}

func TestIsIdentContinue_ASCIIOnly(t *testing.T) {
	// ASCII letters, digits, underscore accepted.
	for _, r := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" {
		if !isIdentContinue(r) {
			t.Errorf("isIdentContinue(%q) = false, want true", r)
		}
	}
	// Non-ASCII rejected.
	for _, r := range []rune{'ä', 'é', 'Ω', '中', '٣'} {
		if isIdentContinue(r) {
			t.Errorf("isIdentContinue(%q) = true, want false", r)
		}
	}
}

func TestParseStylesheet_UppercaseClassNameFailsAtBoundary(t *testing.T) {
	// Verify the error message points to the class name parse.
	_, err := ParseStylesheet(`.Code { llm_model: test; }`)
	if err == nil {
		t.Fatal("expected parse error for uppercase class name")
	}
	if !strings.Contains(err.Error(), "class name") {
		t.Fatalf("error should mention 'class name': %v", err)
	}
}
