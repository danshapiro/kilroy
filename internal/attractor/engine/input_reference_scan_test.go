package engine

import (
	"strings"
	"testing"
)

func TestInputReferenceScan_ExtractsMarkdownQuotedAndBarePaths(t *testing.T) {
	scanner := deterministicInputReferenceScanner{}
	content := strings.Join([]string{
		`Read [tests](docs/tests.md) and also "C:/repo/tests.md".`,
		`Use .ai/definition_of_done.md as source of truth.`,
		`Glob hint: C:/logs/**/*.md`,
		`Quoted glob: '.ai/**/*.md'`,
	}, "\n")

	refs := scanner.Scan(".ai/definition_of_done.md", []byte(content))
	got := map[string]InputReferenceKind{}
	for _, ref := range refs {
		got[ref.Pattern] = ref.Kind
		if ref.Confidence != "explicit" {
			t.Fatalf("confidence: got %q want explicit", ref.Confidence)
		}
	}

	requireRefKind(t, got, "docs/tests.md", InputReferenceKindPath)
	requireRefKind(t, got, "C:/repo/tests.md", InputReferenceKindPath)
	requireRefKind(t, got, ".ai/definition_of_done.md", InputReferenceKindPath)
	requireRefKind(t, got, "C:/logs/**/*.md", InputReferenceKindGlob)
	requireRefKind(t, got, ".ai/**/*.md", InputReferenceKindGlob)
}

func TestInputReferenceScan_IgnoresURLsAndParsesWindowsGlobToken(t *testing.T) {
	scanner := deterministicInputReferenceScanner{}
	refs := scanner.Scan("requirements.md", []byte(`visit https://example.com and scan C:/**/*.md and ./docs/spec.md`))
	got := map[string]InputReferenceKind{}
	for _, ref := range refs {
		got[ref.Pattern] = ref.Kind
	}
	if _, ok := got["https://example.com"]; ok {
		t.Fatalf("URL should not be treated as input reference: %+v", refs)
	}
	requireRefKind(t, got, "C:/**/*.md", InputReferenceKindGlob)
	requireRefKind(t, got, "docs/spec.md", InputReferenceKindPath)
}

func TestInputReferenceScan_RejectsNaturalLanguageWithBrackets(t *testing.T) {
	scanner := deterministicInputReferenceScanner{}
	// These tokens from issue #48 should NOT be classified as globs.
	badTokens := []string{
		`you are wielding ([ch]) [weapon`,
		`DEFAULT_TOOL_LIMITS[tool_name`,
		`array[index`,
		`map[string]any`,
	}
	for _, token := range badTokens {
		refs := scanner.Scan("test.md", []byte(token))
		for _, ref := range refs {
			if ref.Kind == InputReferenceKindGlob {
				t.Errorf("token %q should not be classified as glob, but got pattern=%q kind=%q", token, ref.Pattern, ref.Kind)
			}
		}
	}
}

func TestInputReferenceScan_AcceptsValidGlobBrackets(t *testing.T) {
	scanner := deterministicInputReferenceScanner{}
	// These ARE valid globs with matched brackets.
	content := strings.Join([]string{
		`"src/[abc]/*.go"`,
		`"docs/[a-z]*.md"`,
	}, "\n")
	refs := scanner.Scan("test.md", []byte(content))
	got := map[string]InputReferenceKind{}
	for _, ref := range refs {
		got[ref.Pattern] = ref.Kind
	}
	requireRefKind(t, got, "src/[abc]/*.go", InputReferenceKindGlob)
	requireRefKind(t, got, "docs/[a-z]*.md", InputReferenceKindGlob)
}

func TestIsLikelyArtifactInputPath_ExcludesWorktrees(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".worktrees/rogue-logs/worktree/.cargo-target/foo.rs", true},
		{".worktrees/some-branch/src/main.go", true},
		{"src/.cargo-target/debug/build/foo.o", true},
		{"src/main.go", false},
		{"docs/spec.md", false},
	}
	for _, tc := range cases {
		if got := isLikelyArtifactInputPath(tc.path); got != tc.want {
			t.Errorf("isLikelyArtifactInputPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func requireRefKind(t *testing.T, got map[string]InputReferenceKind, pattern string, want InputReferenceKind) {
	t.Helper()
	kind, ok := got[pattern]
	if !ok {
		t.Fatalf("missing extracted reference %q; got=%v", pattern, got)
	}
	if kind != want {
		t.Fatalf("reference %q kind: got %q want %q", pattern, kind, want)
	}
}
