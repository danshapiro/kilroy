package dot

import "testing"

func TestIsIdentStart_ASCIIOnly(t *testing.T) {
	// ASCII letters and underscore must be accepted.
	for _, r := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ_" {
		if !isIdentStart(r) {
			t.Errorf("isIdentStart(%q) = false, want true", r)
		}
	}
	// Digits must not be accepted as identifier start.
	for _, r := range "0123456789" {
		if isIdentStart(r) {
			t.Errorf("isIdentStart(%q) = true, want false", r)
		}
	}
	// Non-ASCII letters must be rejected (spec: [A-Za-z_]).
	nonASCII := []rune{'ä', 'é', 'ñ', 'ü', 'Ω', '中', 'д', 'α'}
	for _, r := range nonASCII {
		if isIdentStart(r) {
			t.Errorf("isIdentStart(%q) = true, want false (non-ASCII)", r)
		}
	}
}

func TestIsIdentContinue_ASCIIOnly(t *testing.T) {
	// ASCII letters, digits, and underscore must be accepted.
	for _, r := range "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_" {
		if !isIdentContinue(r) {
			t.Errorf("isIdentContinue(%q) = false, want true", r)
		}
	}
	// Non-ASCII letters and digits must be rejected (spec: [A-Za-z0-9_]).
	nonASCII := []rune{'ä', 'é', 'ñ', 'Ω', '中', 'д', '٣', '①'}
	for _, r := range nonASCII {
		if isIdentContinue(r) {
			t.Errorf("isIdentContinue(%q) = true, want false (non-ASCII)", r)
		}
	}
}

func TestLexer_RejectsUnicodeIdentifier(t *testing.T) {
	// A DOT graph with a Unicode node ID must fail to parse.
	src := []byte(`digraph G { café [shape=box] }`)
	_, err := Parse(src)
	if err == nil {
		t.Fatalf("expected parse error for Unicode identifier 'café', got nil")
	}
}

func TestLexer_AcceptsASCIIIdentifiers(t *testing.T) {
	// Standard ASCII identifiers must still work.
	src := []byte(`digraph G {
		start [shape=Mdiamond]
		my_node_1 [shape=box]
		_private [shape=box]
		exit [shape=Msquare]
		start -> my_node_1 -> _private -> exit
	}`)
	g, err := Parse(src)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	for _, id := range []string{"start", "my_node_1", "_private", "exit"} {
		if g.Nodes[id] == nil {
			t.Errorf("node %q not found", id)
		}
	}
}
