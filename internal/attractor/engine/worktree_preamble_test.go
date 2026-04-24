// Tests for BuildWorktreeContextPreamble — the per-run isolation notice that
// pins agents to their worktree so prompts with stray absolute paths don't
// pull them into the user's source tree.
package engine

import (
	"strings"
	"testing"
)

func TestBuildWorktreeContextPreamble_EmptyWhenNoWorktree(t *testing.T) {
	if got := BuildWorktreeContextPreamble(""); got != "" {
		t.Fatalf("empty worktreeDir: got %q, want empty", got)
	}
	if got := BuildWorktreeContextPreamble("   "); got != "" {
		t.Fatalf("whitespace worktreeDir: got %q, want empty", got)
	}
}

func TestBuildWorktreeContextPreamble_IncludesPathAndGuidance(t *testing.T) {
	worktree := "/tmp/kilroy-run-abc/worktree"
	got := BuildWorktreeContextPreamble(worktree)
	if !strings.Contains(got, worktree) {
		t.Fatalf("preamble missing worktree path %q: %s", worktree, got)
	}
	for _, want := range []string{
		"WORKTREE CONTEXT",
		"isolated Kilroy worktree",
		"Do not `cd` elsewhere",
		"informational only",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("preamble missing %q: %s", want, got)
		}
	}
}
