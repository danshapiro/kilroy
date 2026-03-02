package validate

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
)

func loadReferenceTemplate(t *testing.T) []byte {
	t.Helper()
	repoRoot := findRepoRoot(t)
	p := filepath.Join(repoRoot, "skills", "create-dotfile", "reference_template.dot")
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
		case e.From == "check_implement" && e.To == "fix_fmt" && cond == "outcome=success":
			hasCheckSuccessToVerify = true
		}
	}

	if !hasImplementToCheck || !hasCheckFailTransient || !hasCheckFailDeterministic || !hasCheckSuccessToVerify {
		t.Fatalf(
			"missing implement failure-routing guardrails: implement_to_check=%v transient_fail=%v deterministic_fail=%v success_to_verify=%v",
			hasImplementToCheck,
			hasCheckFailTransient,
			hasCheckFailDeterministic,
			hasCheckSuccessToVerify,
		)
	}
}

func TestReferenceTemplate_DeterministicToolGatesHaveZeroRetries(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Deterministic tool gates must have max_retries=0. Re-running a
	// deterministic check on unchanged code always produces the same
	// result, so retries just waste cycles before routing to postmortem.
	deterministicGates := []string{"fix_fmt", "verify_fmt", "verify_artifacts"}
	for _, name := range deterministicGates {
		n := g.Nodes[name]
		if n == nil {
			t.Fatalf("missing node %q", name)
		}
		if got := n.Attr("max_retries", ""); got != "0" {
			t.Errorf("%s: max_retries must be 0 (deterministic check), got %q", name, got)
		}
	}
}

func TestReferenceTemplate_ToolchainGateIsOutcomeControlledWithoutPostmortemRestart(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hasStartToToolchain := false
	hasToolchainSuccessToExpand := false
	hasToolchainDeterministicFailToPostmortem := false
	hasToolchainTransientRestart := false
	hasToolchainBypassToExpand := false
	hasPostmortemRestartToToolchain := false

	for _, e := range g.Edges {
		cond := e.Condition()
		switch {
		case e.From == "start" && e.To == "check_toolchain" && cond == "":
			hasStartToToolchain = true
		case e.From == "check_toolchain" && e.To == "expand_spec" && cond == "outcome=success":
			hasToolchainSuccessToExpand = true
		case e.From == "check_toolchain" && e.To == "postmortem" && cond == "outcome=fail && context.failure_class!=transient_infra":
			hasToolchainDeterministicFailToPostmortem = true
		case e.From == "check_toolchain" && e.To == "check_toolchain" && cond == "outcome=fail && context.failure_class=transient_infra" && e.Attr("loop_restart", "false") == "true":
			hasToolchainTransientRestart = true
		case e.From == "check_toolchain" && e.To == "expand_spec" && cond == "":
			hasToolchainBypassToExpand = true
		case e.From == "postmortem" && e.To == "check_toolchain" && e.Attr("loop_restart", "false") == "true":
			hasPostmortemRestartToToolchain = true
		}
	}

	if !hasStartToToolchain ||
		!hasToolchainSuccessToExpand ||
		!hasToolchainDeterministicFailToPostmortem ||
		!hasToolchainTransientRestart ||
		hasToolchainBypassToExpand ||
		hasPostmortemRestartToToolchain {
		t.Fatalf(
			"missing/broken toolchain gate routing: start_to_toolchain=%v success_to_expand=%v deterministic_fail_to_postmortem=%v transient_restart=%v bypass_to_expand=%v postmortem_restart_to_toolchain=%v",
			hasStartToToolchain,
			hasToolchainSuccessToExpand,
			hasToolchainDeterministicFailToPostmortem,
			hasToolchainTransientRestart,
			hasToolchainBypassToExpand,
			hasPostmortemRestartToToolchain,
		)
	}
}

func TestReferenceTemplate_PostmortemRecoveryRouting_IsDomainRouted(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hasImplRepairToImplement := false
	hasNeedsReplanToPlanFanout := false
	hasNeedsToolchainToToolchain := false
	hasTransientFailToToolchain := false
	hasFallbackToImplement := false
	hasUnconditionalToToolchain := false

	for _, e := range g.Edges {
		cond := strings.TrimSpace(e.Condition())
		switch {
		case e.From == "postmortem" && e.To == "implement" && cond == "outcome=impl_repair":
			hasImplRepairToImplement = true
		case e.From == "postmortem" && e.To == "plan_fanout" && cond == "outcome=needs_replan":
			hasNeedsReplanToPlanFanout = true
		case e.From == "postmortem" && e.To == "check_toolchain" && cond == "outcome=needs_toolchain":
			hasNeedsToolchainToToolchain = true
		case e.From == "postmortem" && e.To == "check_toolchain" && cond == "outcome=fail && context.failure_class=transient_infra":
			hasTransientFailToToolchain = true
		case e.From == "postmortem" && e.To == "implement" && cond == "":
			hasFallbackToImplement = true
		case e.From == "postmortem" && e.To == "check_toolchain" && cond == "":
			hasUnconditionalToToolchain = true
		}
	}

	if !hasImplRepairToImplement ||
		!hasNeedsReplanToPlanFanout ||
		!hasNeedsToolchainToToolchain ||
		!hasTransientFailToToolchain ||
		!hasFallbackToImplement ||
		hasUnconditionalToToolchain {
		t.Fatalf(
			"missing/broken postmortem routing: impl_repair=%v needs_replan_plan_fanout=%v needs_toolchain=%v transient_fail=%v fallback_implement=%v unconditional_toolchain=%v",
			hasImplRepairToImplement,
			hasNeedsReplanToPlanFanout,
			hasNeedsToolchainToToolchain,
			hasTransientFailToToolchain,
			hasFallbackToImplement,
			hasUnconditionalToToolchain,
		)
	}
}

func TestReferenceTemplate_HasAutoFixBeforeVerifyFmt(t *testing.T) {
	g, err := dot.Parse(loadReferenceTemplate(t))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	fixFmt := g.Nodes["fix_fmt"]
	if fixFmt == nil {
		t.Fatal("missing fix_fmt node")
	}
	if fixFmt.Shape() != "parallelogram" {
		t.Fatalf("fix_fmt shape must be parallelogram, got %q", fixFmt.Shape())
	}

	hasCheckImplementToFixFmt := false
	hasFixFmtToVerifyFmt := false
	for _, e := range g.Edges {
		switch {
		case e.From == "check_implement" && e.To == "fix_fmt" && e.Condition() == "outcome=success":
			hasCheckImplementToFixFmt = true
		case e.From == "fix_fmt" && e.To == "verify_fmt":
			hasFixFmtToVerifyFmt = true
		}
	}
	if !hasCheckImplementToFixFmt || !hasFixFmtToVerifyFmt {
		t.Fatalf("missing fix_fmt routing: check_implement_to_fix_fmt=%v fix_fmt_to_verify_fmt=%v",
			hasCheckImplementToFixFmt, hasFixFmtToVerifyFmt)
	}
}

func TestReferenceTemplate_PostmortemPromptClarifiesStatusContract(t *testing.T) {
	template := loadReferenceTemplate(t)
	g, err := dot.Parse(template)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	pm := g.Nodes["postmortem"]
	if pm == nil {
		t.Fatal("missing postmortem node")
	}
	templateText := string(template)
	const startMarker = "// PROMPT: postmortem"
	const endMarker = "postmortem [auto_status=true]"
	const requiredText = "status reflects analysis completion, not implementation state"

	start := strings.Index(templateText, startMarker)
	if start < 0 {
		t.Fatal("missing postmortem prompt guidance block in reference template")
	}
	section := templateText[start:]
	end := strings.Index(section, endMarker)
	if end < 0 {
		t.Fatal("missing postmortem node declaration after guidance block")
	}

	if !strings.Contains(section[:end], requiredText) {
		t.Fatal("postmortem template guidance must clarify that status reflects analysis completion, not implementation state")
	}
}

func TestReferenceSurfaces_NoLegacyRootAIScratchPaths(t *testing.T) {
	repoRoot := findRepoRoot(t)
	referenceSurfaces := []string{
		"skills/create-dotfile/SKILL.md",
		"skills/create-dotfile/reference_template.dot",
		"skills/build-dod/SKILL.md",
		"skills/create-runfile/SKILL.md",
		"skills/create-runfile/reference_run_template.yaml",
		"demo/substack-spec-v01.dot",
		"docs/strongdm/dot specs/semport.dot",
		"docs/strongdm/attractor/attractor-spec.md",
		"docs/strongdm/attractor/README.md",
	}

	legacyNeedles := []string{
		".ai/postmortem_latest.md",
		".ai/review_final.md",
	}
	aiPathPattern := regexp.MustCompile(`(?:^|[^A-Za-z0-9_])(\.ai/)`)

	for _, relPath := range referenceSurfaces {
		fullPath := filepath.Join(repoRoot, filepath.FromSlash(relPath))
		b, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("%s: read: %v", relPath, err)
		}
		text := string(b)

		for _, needle := range legacyNeedles {
			if strings.Contains(text, needle) {
				t.Errorf("%s: contains legacy root scratch guidance %q; use run-scoped .ai/runs/$KILROY_RUN_ID/... paths", relPath, needle)
			}
		}

		var nonRunScopedSnippets []string
		matches := aiPathPattern.FindAllStringSubmatchIndex(text, -1)
		for _, match := range matches {
			idx := match[2]
			tail := text[idx:]
			if !strings.HasPrefix(tail, ".ai/runs/$KILROY_RUN_ID/") &&
				!strings.HasPrefix(tail, ".ai/runs/${KILROY_RUN_ID}/") {
				end := idx + 100
				if end > len(text) {
					end = len(text)
				}
				snippet := strings.ReplaceAll(text[idx:end], "\n", "\\n")
				nonRunScopedSnippets = append(nonRunScopedSnippets, snippet)
			}
		}

		if len(nonRunScopedSnippets) > 0 {
			t.Errorf("%s: found non-run-scoped .ai/ scratch references (examples: %q)", relPath, nonRunScopedSnippets)
		}
	}
}
