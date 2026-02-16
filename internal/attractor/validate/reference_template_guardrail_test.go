package validate

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
)

func loadReferenceTemplate(t *testing.T) []byte {
	t.Helper()
	repoRoot := findRepoRoot(t)
	p := filepath.Join(repoRoot, "skills", "english-to-dotfile", "reference_template.dot")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read reference template: %v", err)
	}
	return b
}

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}

func TestReferenceTemplate_ImplementNodeDisablesAllowPartial(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	impl := g.Nodes["implement"]
	if impl == nil {
		t.Fatal("missing implement node")
	}
	if impl.Attr("allow_partial", "false") == "true" {
		t.Fatal("implement.allow_partial must not be true")
	}
}

func TestReferenceTemplate_ImplementFailureRoutedBeforeVerify(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hasImplementToCheck := false
	hasCheckFailTransient := false
	hasCheckFailDeterministic := false
	hasCheckSuccessToVerify := false

	for _, e := range g.Edges {
		cond := e.Condition()
		switch {
		case e.From == "implement" && e.To == "check_implement" && cond == "":
			hasImplementToCheck = true
		case e.From == "check_implement" && e.To == "implement" && cond == "outcome=fail && context.failure_class=transient_infra" && e.Attr("loop_restart", "false") == "true":
			hasCheckFailTransient = true
		case e.From == "check_implement" && e.To == "postmortem" && cond == "outcome=fail && context.failure_class!=transient_infra":
			hasCheckFailDeterministic = true
		case e.From == "check_implement" && e.To == "verify_fmt" && cond == "outcome=success":
			hasCheckSuccessToVerify = true
		}
	}

	if !hasImplementToCheck || !hasCheckFailTransient || !hasCheckFailDeterministic || !hasCheckSuccessToVerify {
		t.Fatalf("missing implement failure-routing guardrails")
	}
}
