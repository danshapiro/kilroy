package validate

import (
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
)

func TestValidate_StartAndExitNodeRules(t *testing.T) {
	// Missing start node.
	g1, err := dot.Parse([]byte(`digraph G { exit [shape=Msquare] }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d1 := Validate(g1)
	assertHasRule(t, d1, "start_node", SeverityError)

	// Missing exit node.
	g2, err := dot.Parse([]byte(`digraph G { start [shape=Mdiamond] }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	d2 := Validate(g2)
	assertHasRule(t, d2, "terminal_node", SeverityError)
}

func TestValidate_ReachabilityAndEdgeTargets(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  orphan [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  start -> a -> exit
  a -> missing
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "reachability", SeverityError)
	assertHasRule(t, diags, "edge_target_exists", SeverityError)

	// Spec DoD: lint diagnostics include node/edge IDs.
	foundNode := false
	foundEdge := false
	for _, d := range diags {
		if d.Rule == "reachability" && strings.TrimSpace(d.NodeID) != "" {
			foundNode = true
		}
		if d.Rule == "edge_target_exists" && (strings.TrimSpace(d.EdgeFrom) != "" || strings.TrimSpace(d.EdgeTo) != "") {
			foundEdge = true
		}
	}
	if !foundNode {
		t.Fatalf("expected reachability diagnostic to include node_id")
	}
	if !foundEdge {
		t.Fatalf("expected edge_target_exists diagnostic to include edge ids")
	}
}

func TestValidate_StartNoIncomingAndExitNoOutgoing(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  start -> a -> exit
  a -> start
  exit -> a
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "start_no_incoming", SeverityError)
	assertHasRule(t, diags, "exit_no_outgoing", SeverityError)
}

func TestValidate_ConditionAndStylesheetSyntax(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [model_stylesheet="* { llm_provider: openai; } box { llm_model: gpt-5.4; }"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  start -> a -> exit
  a -> exit [condition="outcome>success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "condition_syntax", SeverityError)
}

func TestValidate_LLMProviderRequired_Metaspec(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_model=gpt-5.4]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "llm_provider_required", SeverityError)
	for _, d := range diags {
		if d.Rule == "llm_provider_required" && d.Fix == "" {
			t.Error("llm_provider_required diagnostic should include a Fix hint")
		}
	}
}

func TestValidate_ToolCommandRequired_ParallelogramWithToolCommand_NoError(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [shape=parallelogram, tool_command="echo ok"]
  start -> t -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "tool_command_required")
}

func TestValidate_ToolCommandRequired_ParallelogramWithCommandOnly_Error(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [shape=parallelogram, command="echo ok"]
  start -> t -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "tool_command_required", SeverityError)
}

func TestValidate_ToolCommandRequired_TypeToolRequiresToolCommand(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [shape=box, type=tool, prompt="ignored for tool"]
  start -> t -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "tool_command_required", SeverityError)
}

func TestValidate_PromptOnAgentNodes_WarnsWhenMissingPrompt(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	found := false
	for _, d := range diags {
		if d.Rule == "prompt_on_llm_nodes" && d.Severity == SeverityWarning && d.NodeID == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected prompt_on_llm_nodes WARNING for node a; got %+v", diags)
	}
}

func TestValidate_LoopRestartFailureEdgeRequiresTransientInfraGuard(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  start -> a -> check
  check -> a [condition="outcome=fail", loop_restart=true]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_LoopRestartFailureEdgeWithTransientInfraGuard_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  pm [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="postmortem"]
  start -> a -> check
  check -> a [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
  check -> pm [condition="outcome=fail && context.failure_class!=transient_infra"]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	for _, d := range diags {
		if d.Rule == "loop_restart_failure_class_guard" {
			t.Fatalf("unexpected loop_restart_failure_class_guard warning: %+v", d)
		}
	}
}

func TestValidate_LoopRestartOnUnconditionalEdge_Warns(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="y"]
  start -> a -> b
  b -> a [loop_restart=true]
  a -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_LoopRestartTransientGuardWithoutDeterministicFallback_Warns(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  start -> a -> check
  check -> a [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_LoopRestartTransientCompanionIsNotDeterministicFallback_Warns(t *testing.T) {
	// A non-restart companion edge that is ALSO guarded by transient_infra
	// does not satisfy the "non-restart deterministic fail edge" requirement.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  pm [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="postmortem"]
  start -> a -> check
  check -> a [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
  check -> pm [condition="outcome=fail && context.failure_class=transient_infra"]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_LoopRestartPartialSuccessCompanionIsNotDeterministicFallback_Warns(t *testing.T) {
	// A non-restart companion conditioned on outcome=partial_success does not
	// route outcome=fail traffic, so it is not a valid deterministic fallback.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  pm [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="postmortem"]
  start -> a -> check
  check -> a [condition="outcome=fail && context.failure_class=transient_infra", loop_restart=true]
  check -> pm [condition="outcome=partial_success"]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "loop_restart_failure_class_guard", SeverityWarning)
}

func TestValidate_FailLoopFailureClassGuard_WarnsWhenBackEdgeUnguarded(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  impl [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  start -> impl -> check
  check -> impl [condition="outcome=fail"]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "fail_loop_failure_class_guard", SeverityWarning)
}

func TestValidate_FailLoopFailureClassGuard_NoWarningWhenFailureClassGuarded(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  impl [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  check [shape=diamond]
  start -> impl -> check
  check -> impl [condition="outcome=fail && context.failure_class=transient_infra"]
  check -> postmortem [condition="outcome=fail && context.failure_class!=transient_infra"]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="p"]
  check -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	for _, d := range diags {
		if d.Rule == "fail_loop_failure_class_guard" {
			t.Fatalf("unexpected fail_loop_failure_class_guard warning: %+v", d)
		}
	}
}

func TestValidate_EscalationModelsSyntax_Valid_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x", escalation_models="kimi:kimi-k2.5, anthropic:claude-opus-4-6"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	for _, d := range diags {
		if d.Rule == "escalation_models_syntax" {
			t.Fatalf("unexpected escalation_models_syntax warning for valid entries: %+v", d)
		}
	}
}

func TestValidate_EscalationModelsSyntax_MissingColon(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x", escalation_models="kimi-kimi-k2.5"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "escalation_models_syntax", SeverityWarning)
}

func TestValidate_EscalationModelsSyntax_EmptyProvider(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x", escalation_models=":some-model"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "escalation_models_syntax", SeverityWarning)
}

func TestValidate_ShapeAliases_DownstreamLintsFireForCircleAndDoublecircle(t *testing.T) {
	// circle=start, doublecircle=exit aliases should be recognized by downstream lints
	// (start_no_incoming, exit_no_outgoing, reachability) not just lintStartNode/lintExitNode.
	g, err := dot.Parse([]byte(`
digraph G {
  s [shape=circle]
  e [shape=doublecircle]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  s -> a -> e
  a -> s
  e -> a
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	// Should fire start_no_incoming (a->s) and exit_no_outgoing (e->a).
	assertHasRule(t, diags, "start_no_incoming", SeverityError)
	assertHasRule(t, diags, "exit_no_outgoing", SeverityError)

	// Reachability should also work — all nodes reachable, so no reachability errors.
	for _, d := range diags {
		if d.Rule == "reachability" {
			t.Fatalf("unexpected reachability error for fully connected alias-shaped graph: %+v", d)
		}
	}
}

func TestValidate_GoalGateExitStatusContract_ErrorAndPromptWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target=implement]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  review_consensus [
    shape=box,
    goal_gate=true,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Review and decide outcome.\nWrite status JSON with outcome=pass when approved."
  ]
  start -> review_consensus
  review_consensus -> exit [condition="outcome=pass"]
  review_consensus -> implement [condition="outcome=retry"]
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "goal_gate_exit_status_contract", SeverityError)
	assertHasRule(t, diags, "goal_gate_prompt_status_hint", SeverityWarning)
}

func TestValidate_GoalGateExitStatusContract_AllowsCanonicalSuccessOutcomes(t *testing.T) {
	tests := []struct {
		name         string
		exitOutcome  string
		promptStatus string
	}{
		{
			name:         "success",
			exitOutcome:  "success",
			promptStatus: "success",
		},
		{
			name:         "partial_success",
			exitOutcome:  "partial_success",
			promptStatus: "partial_success",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dotSrc := `
digraph G {
  graph [retry_target=implement]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  review_consensus [
    shape=box,
    goal_gate=true,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Review and decide outcome.\nWrite status JSON with outcome=` + tc.promptStatus + ` when approved."
  ]
  start -> review_consensus
  review_consensus -> exit [condition="outcome=` + tc.exitOutcome + `"]
  review_consensus -> implement [condition="outcome=retry"]
  implement -> exit [condition="outcome=success"]
}
`
			g, err := dot.Parse([]byte(dotSrc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			diags := Validate(g)
			assertNoRule(t, diags, "goal_gate_exit_status_contract")
			assertNoRule(t, diags, "goal_gate_prompt_status_hint")
		})
	}
}

func TestValidate_GoalGateExitStatusContract_NoTerminalMismatchNoError(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target=implement]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  review_consensus [
    shape=box,
    goal_gate=true,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Review and decide outcome.\nWrite status JSON with outcome=success when approved."
  ]
  review_router [shape=diamond]
  start -> review_consensus
  review_consensus -> review_router [condition="outcome=pass"]
  review_router -> exit [condition="outcome=success"]
  review_consensus -> implement [condition="outcome=retry"]
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "goal_gate_exit_status_contract")
	assertNoRule(t, diags, "goal_gate_prompt_status_hint")
}

func TestValidate_TemplateProvenancePostmortemRouting_WarnsOnNeedsReplanToNonPlanningEntry(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [provenance_version="1"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="pm"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  debate_consolidate [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="debate"]
  start -> postmortem
  postmortem -> debate_consolidate [condition="outcome=needs_replan"]
  postmortem -> implement
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "template_postmortem_replan_entry", SeverityWarning)
}

func TestValidate_TemplateProvenancePostmortemRouting_NoWarningWithoutProvenance(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="pm"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  debate_consolidate [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="debate"]
  check_toolchain [shape=parallelogram, tool_command="echo ok"]
  start -> postmortem
  postmortem -> debate_consolidate [condition="outcome=needs_replan"]
  postmortem -> check_toolchain
  postmortem -> implement
  implement -> exit [condition="outcome=success"]
  check_toolchain -> implement
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "template_postmortem_replan_entry")
	assertNoRule(t, diags, "template_postmortem_broad_rollback")
}

func TestValidate_TemplateProvenancePostmortemRouting_WarnsOnUnconditionalToolchainRollback(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [provenance_version="1"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="pm"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  plan_fanout [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="plan"]
  check_toolchain [shape=parallelogram, tool_command="echo ok"]
  start -> postmortem
  postmortem -> plan_fanout [condition="outcome=needs_replan"]
  postmortem -> check_toolchain
  postmortem -> implement
  implement -> exit [condition="outcome=success"]
  plan_fanout -> implement
  check_toolchain -> implement
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "template_postmortem_broad_rollback", SeverityWarning)
	assertNoRule(t, diags, "template_postmortem_replan_entry")
}

func TestValidate_TemplateProvenancePostmortemRouting_WarnsWhenMissingNeedsReplanRoute(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [provenance_version="1"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="pm"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  start -> postmortem
  postmortem -> implement [condition="outcome=impl_repair"]
  postmortem -> implement
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "template_postmortem_replan_entry", SeverityWarning)
}

// TestLintConditionSyntax_ValidConditions verifies that well-formed condition
// expressions do not produce condition_syntax diagnostics — confirming that the
// evaluator error-capture path (G2 fix) does not create false positives.
func TestLintConditionSyntax_ValidConditions(t *testing.T) {
	validConds := []string{
		"outcome=success",
		"outcome=fail",
		"outcome!=success",
		"outcome=success && outcome!=fail",
		"outcome=approved",
		"context.failure_class=transient_infra",
		"context.failure_class!=transient_infra",
		"preferred_label=Yes",
		"my_key=some_value",
	}

	for _, cond := range validConds {
		cond := cond
		t.Run(cond, func(t *testing.T) {
			dotSrc := `
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> exit [condition="` + cond + `"]
}
`
			g, err := dot.Parse([]byte(dotSrc))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			diags := lintConditionSyntax(g)
			for _, d := range diags {
				t.Errorf("unexpected condition_syntax diagnostic for %q: %s %s", cond, d.Severity, d.Message)
			}
		})
	}
}

// TestLintConditionSyntax_SyntaxRejectsGreaterThanOperator verifies that
// "outcome>success" produces a condition_syntax ERROR. Note: cond.Evaluate
// is never reached for inputs that fail validateConditionSyntax, so this
// test exercises only the syntax-checker path. The Evaluate error paths in
// evalClause are unreachable for inputs that pass syntax check (SplitN with
// a known operator always returns exactly 2 parts); the guard remains as a
// forward-compatibility safety net.
func TestLintConditionSyntax_SyntaxRejectsGreaterThanOperator(t *testing.T) {
	// Invalid condition: uses ">" which validateConditionSyntax rejects.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> exit [condition="outcome>success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintConditionSyntax(g)
	assertHasRule(t, diags, "condition_syntax", SeverityError)

	// Valid condition: must produce no condition_syntax diagnostic.
	g2, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags2 := lintConditionSyntax(g2)
	for _, d := range diags2 {
		t.Errorf("unexpected condition_syntax diagnostic for valid condition: %+v", d)
	}
}

func assertHasRule(t *testing.T, diags []Diagnostic, rule string, sev Severity) {
	t.Helper()
	for _, d := range diags {
		if d.Rule == rule && d.Severity == sev {
			return
		}
	}
	var got []string
	for _, d := range diags {
		got = append(got, string(d.Severity)+":"+d.Rule)
	}
	t.Fatalf("expected %s:%s; got %s", sev, rule, strings.Join(got, ", "))
}

func assertNoRule(t *testing.T, diags []Diagnostic, rule string) {
	t.Helper()
	for _, d := range diags {
		if d.Rule == rule {
			t.Fatalf("unexpected diagnostic %s:%s (%s)", d.Severity, d.Rule, d.Message)
		}
	}
}

// --- Tests for tool_command_abs_path lint rule ---

func TestValidate_ToolCommandAbsPath_WarnsOnCdAbsolutePath(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [shape=parallelogram, tool_command="cd /tmp && echo hello"]
  start -> t -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "tool_command_abs_path", SeverityWarning)
}

func TestValidate_ToolCommandAbsPath_NoWarningOnRelativeCommand(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [shape=parallelogram, tool_command="cargo test"]
  start -> t -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "tool_command_abs_path")
}

// --- Tests for V7.2: type_known lint rule ---

func TestValidate_TypeKnownRule_RecognizedType_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, type=agent, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rule := NewTypeKnownRule([]string{"agent", "conditional", "start", "exit"})
	diags := Validate(g, rule)
	assertNoRule(t, diags, "type_known")
}

func TestValidate_TypeKnownRule_UnrecognizedType_Warning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, type=unknown_handler, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rule := NewTypeKnownRule([]string{"agent", "conditional", "start", "exit"})
	diags := Validate(g, rule)
	assertHasRule(t, diags, "type_known", SeverityWarning)
}

func TestValidate_TypeKnownRule_NoTypeOverride_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	rule := NewTypeKnownRule([]string{"agent"})
	diags := Validate(g, rule)
	assertNoRule(t, diags, "type_known")
}

// --- Tests for V7.3/V7.4: LintRule interface and extra_rules ---

type testLintRule struct {
	name string
	diag Diagnostic
}

func (r *testLintRule) Name() string                      { return r.name }
func (r *testLintRule) Apply(g *model.Graph) []Diagnostic { return []Diagnostic{r.diag} }

func TestValidate_ExtraRules_AreAppendedToBuiltInRules(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	custom := &testLintRule{
		name: "custom_test_rule",
		diag: Diagnostic{Rule: "custom_test_rule", Severity: SeverityInfo, Message: "test"},
	}
	diags := Validate(g, custom)
	assertHasRule(t, diags, "custom_test_rule", SeverityInfo)
}

func TestValidate_ExtraRules_NilRulesIgnored(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Should not panic on nil rules.
	_ = Validate(g, nil)
}

// --- Tests for V7.5: ValidateOrError collects all errors ---

func TestValidateOrError_CollectsAllErrors(t *testing.T) {
	// Graph with multiple validation errors: no start, no exit, edge to missing node.
	g, err := dot.Parse([]byte(`
digraph G {
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  a -> missing
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	vErr := ValidateOrError(g)
	if vErr == nil {
		t.Fatal("expected validation error")
	}
	msg := vErr.Error()
	// Should contain multiple errors, not just the first one.
	if !strings.Contains(msg, "start_node") {
		t.Fatalf("expected start_node error in message: %s", msg)
	}
	if !strings.Contains(msg, "terminal_node") {
		t.Fatalf("expected terminal_node error in message: %s", msg)
	}
	if !strings.Contains(msg, "edge_target_exists") {
		t.Fatalf("expected edge_target_exists error in message: %s", msg)
	}
}

// --- Tests for V7.6: at-least-one exit node ---

func TestValidate_MultipleExitNodes_NoError(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  success_exit [shape=Msquare]
  error_exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> success_exit [condition="outcome=success"]
  a -> error_exit [condition="outcome=fail"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "terminal_node")
}

func TestValidate_ZeroExitNodes_Error(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  start -> a
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "terminal_node", SeverityError)
}

func TestValidate_MultipleExitNodes_ExitNoOutgoingChecksAll(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit1 [shape=Msquare]
  exit2 [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> exit1 [condition="outcome=success"]
  a -> exit2 [condition="outcome=fail"]
  exit2 -> a
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	// exit2 has an outgoing edge, so exit_no_outgoing should fire.
	assertHasRule(t, diags, "exit_no_outgoing", SeverityError)
	// Verify the diagnostic points at exit2, not exit1.
	for _, d := range diags {
		if d.Rule == "exit_no_outgoing" && d.NodeID == "exit2" {
			return
		}
	}
	t.Fatal("expected exit_no_outgoing diagnostic for exit2")
}

// --- Tests for status_contract_in_prompt lint rule (G1) ---

// (a) shape=box, non-empty prompt, missing KILROY_STAGE_STATUS_PATH → WARNING fires.
func TestValidate_StatusContractInPrompt_MissingContract_Warning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  impl  [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4-6,
         prompt="Implement the feature and write the output to disk."]
  start -> impl -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	found := false
	for _, d := range diags {
		if d.Rule == "status_contract_in_prompt" && d.Severity == SeverityWarning && d.NodeID == "impl" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected status_contract_in_prompt WARNING for node impl; got %+v", diags)
	}
}

// (b) shape=box, prompt contains $KILROY_STAGE_STATUS_PATH → no warning.
func TestValidate_StatusContractInPrompt_PrimaryPath_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  impl  [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4-6,
         prompt="Implement and write {\"outcome\":\"success\"} to $KILROY_STAGE_STATUS_PATH."]
  start -> impl -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_contract_in_prompt")
}

// (c) shape=box, prompt contains $KILROY_STAGE_STATUS_FALLBACK_PATH but NOT primary → no warning.
func TestValidate_StatusContractInPrompt_FallbackPath_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  impl  [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4-6,
         prompt="Write outcome to $KILROY_STAGE_STATUS_FALLBACK_PATH if primary is unavailable."]
  start -> impl -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_contract_in_prompt")
}

// (d) shape=box, empty prompt → no warning from status_contract_in_prompt (existing rule handles it).
func TestValidate_StatusContractInPrompt_EmptyPrompt_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  impl  [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4-6]
  start -> impl -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_contract_in_prompt")
	// The existing empty-prompt rule should still fire.
	assertHasRule(t, diags, "prompt_on_llm_nodes", SeverityWarning)
}

// (e) non-box node (shape=diamond) with prompt missing contract → no warning from this rule.
func TestValidate_StatusContractInPrompt_NonBoxNode_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  router [shape=diamond, prompt="This prompt has no status path reference."]
  impl   [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4-6,
          prompt="Write outcome to $KILROY_STAGE_STATUS_PATH."]
  start -> impl -> router
  router -> exit [condition="outcome=success"]
  router -> impl [condition="outcome=fail"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	// The status_contract_in_prompt rule must NOT fire for the diamond node.
	for _, d := range diags {
		if d.Rule == "status_contract_in_prompt" && d.NodeID == "router" {
			t.Fatalf("unexpected status_contract_in_prompt warning for diamond node router: %+v", d)
		}
	}
}

// --- Tests for orphan_custom_outcome_hint lint rule (G5) ---

// (a) Node with condition="outcome=approved" edge (custom) + no unconditional fallback -> WARNING fires.
func TestValidate_OrphanCustomOutcomeHint_CustomOutcomeNoFallback_Warns(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  review [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="review"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  start -> review
  review -> exit [condition="outcome=approved"]
  review -> implement [condition="outcome=retry"]
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	found := false
	for _, d := range diags {
		if d.Rule == "orphan_custom_outcome_hint" && d.Severity == SeverityWarning && d.NodeID == "review" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected orphan_custom_outcome_hint WARNING for node 'review'; got %+v", diags)
	}
}

// (b) Node with condition="outcome=approved" edge + unconditional fallback -> no warning.
func TestValidate_OrphanCustomOutcomeHint_CustomOutcomeWithFallback_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  review [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="review"]
  implement [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="impl"]
  start -> review
  review -> exit [condition="outcome=approved"]
  review -> implement
  implement -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "orphan_custom_outcome_hint")
}

// (c) Node with only condition="status=success" (reserved status key, no custom outcome) -> no warning.
func TestValidate_OrphanCustomOutcomeHint_ReservedOutcomeOnly_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  postmortem [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="pm"]
  start -> a
  a -> exit [condition="outcome=success"]
  a -> postmortem [condition="outcome=fail"]
  postmortem -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "orphan_custom_outcome_hint")
}

// (d) Node with no conditional edges at all -> no warning.
func TestValidate_OrphanCustomOutcomeHint_NoConditionalEdges_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="y"]
  start -> a -> b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "orphan_custom_outcome_hint")
}

// --- Tests for status_fallback_in_prompt lint rule (G11) ---

// TestValidate_StatusFallbackInPrompt_WarnsWhenPrimaryPresentButFallbackAbsent verifies
// that the rule fires when a box node's prompt has KILROY_STAGE_STATUS_PATH but not
// KILROY_STAGE_STATUS_FALLBACK_PATH (the rogue-08 audit scenario: 16/16 nodes affected).
func TestValidate_StatusFallbackInPrompt_WarnsWhenPrimaryPresentButFallbackAbsent(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [
    shape=box,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Write your result to $KILROY_STAGE_STATUS_PATH when done."
  ]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	found := false
	for _, d := range diags {
		if d.Rule == "status_fallback_in_prompt" && d.Severity == SeverityWarning && d.NodeID == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected status_fallback_in_prompt WARNING for node a; got %+v", diags)
	}
}

// TestValidate_StatusFallbackInPrompt_NoWarnWhenBothPathsPresent verifies that the rule
// does not fire when both the primary and fallback paths are present in the prompt.
func TestValidate_StatusFallbackInPrompt_NoWarnWhenBothPathsPresent(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [
    shape=box,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Write your result to $KILROY_STAGE_STATUS_PATH; fallback is $KILROY_STAGE_STATUS_FALLBACK_PATH."
  ]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_fallback_in_prompt")
}

// TestValidate_StatusFallbackInPrompt_NoWarnWhenNeitherPathPresent verifies that the rule
// does NOT fire when neither path is in the prompt (G1 handles that case).
func TestValidate_StatusFallbackInPrompt_NoWarnWhenNeitherPathPresent(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [
    shape=box,
    llm_provider=openai,
    llm_model=gpt-5.4,
    prompt="Do some work and report your findings."
  ]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_fallback_in_prompt")
}

// TestValidate_StatusFallbackInPrompt_NoWarnWhenAutoStatusTrue verifies that the rule
// is suppressed when auto_status=true, since the engine manages status automatically.
func TestValidate_StatusFallbackInPrompt_NoWarnWhenAutoStatusTrue(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [
    shape=box,
    llm_provider=openai,
    llm_model=gpt-5.4,
    auto_status=true,
    prompt="Write your result to $KILROY_STAGE_STATUS_PATH when done."
  ]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_fallback_in_prompt")
}

// --- Tests for G12: stylesheet model ID catalog validation ---

// buildTestCatalog creates a minimal in-memory catalog for testing.
func buildTestCatalog() *modeldb.Catalog {
	return &modeldb.Catalog{
		Models: map[string]modeldb.ModelEntry{
			"anthropic/claude-opus-4.6":   {Provider: "anthropic"},
			"anthropic/claude-sonnet-4.5": {Provider: "anthropic"},
			"openai/gpt-5.4":              {Provider: "openai"},
		},
		CoveredProviders: map[string]bool{
			"anthropic": true,
			"openai":    true,
		},
	}
}

// minimalGraph returns a valid minimal graph with a model_stylesheet attribute set.
func minimalGraphWithStylesheet(stylesheet string) []byte {
	return []byte(`digraph G {
  graph [model_stylesheet="` + stylesheet + `"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}`)
}

// TestValidate_G12_UnknownModelID_EmitsWarning checks that a completely unknown
// model ID in the stylesheet produces a stylesheet_unknown_model WARNING.
func TestValidate_G12_UnknownModelID_EmitsWarning(t *testing.T) {
	g, err := dot.Parse(minimalGraphWithStylesheet(`* { llm_provider: anthropic; llm_model: claude-opus-999-nonexistent; }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	catalog := buildTestCatalog()
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: catalog})
	assertHasRule(t, diags, "stylesheet_unknown_model", SeverityWarning)
}

// TestValidate_G12_DashedAnthropicModelID_EmitsNonCanonicalError checks that
// claude-opus-4-6 (dashed version number) produces a stylesheet_noncanonical_model_id
// ERROR because the catalog canonical form is claude-opus-4.6 (dotted).
func TestValidate_G12_DashedAnthropicModelID_EmitsNonCanonicalError(t *testing.T) {
	g, err := dot.Parse(minimalGraphWithStylesheet(`* { llm_provider: anthropic; llm_model: claude-opus-4-6; }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	catalog := buildTestCatalog()
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: catalog})
	assertHasRule(t, diags, "stylesheet_noncanonical_model_id", SeverityError)
	assertNoRule(t, diags, "stylesheet_unknown_model")
}

// TestValidate_G12_CanonicalModelID_NoWarning checks that a valid canonical model
// ID in the stylesheet produces no stylesheet_unknown_model or
// stylesheet_noncanonical_model_id warnings.
func TestValidate_G12_CanonicalModelID_NoWarning(t *testing.T) {
	g, err := dot.Parse(minimalGraphWithStylesheet(`* { llm_provider: anthropic; llm_model: claude-opus-4.6; }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	catalog := buildTestCatalog()
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: catalog})
	assertNoRule(t, diags, "stylesheet_unknown_model")
	assertNoRule(t, diags, "stylesheet_noncanonical_model_id")
}

// TestValidate_G12_NoStylesheet_NoWarning checks that when no model_stylesheet is
// present the catalog check produces no diagnostics.
func TestValidate_G12_NoStylesheet_NoWarning(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	catalog := buildTestCatalog()
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: catalog})
	assertNoRule(t, diags, "stylesheet_unknown_model")
	assertNoRule(t, diags, "stylesheet_noncanonical_model_id")
}

// TestValidate_G12_NilCatalog_NoWarning checks that when no catalog is provided
// the model ID checks are silently skipped.
func TestValidate_G12_NilCatalog_NoWarning(t *testing.T) {
	g, err := dot.Parse(minimalGraphWithStylesheet(`* { llm_provider: anthropic; llm_model: claude-totally-bogus; }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: nil})
	assertNoRule(t, diags, "stylesheet_unknown_model")
	assertNoRule(t, diags, "stylesheet_noncanonical_model_id")
}

// TestValidate_G12_UnknownProvider_NoWarning checks that when the catalog does not
// cover the provider, no unknown-model warning is emitted (the catalog has no opinion).
func TestValidate_G12_UnknownProvider_NoWarning(t *testing.T) {
	g, err := dot.Parse(minimalGraphWithStylesheet(`* { llm_provider: cerebras; llm_model: llama-4-scout; }`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	catalog := buildTestCatalog() // cerebras not covered
	diags := ValidateWithOptions(g, ValidateOptions{Catalog: catalog})
	assertNoRule(t, diags, "stylesheet_unknown_model")
	assertNoRule(t, diags, "stylesheet_noncanonical_model_id")
}

// TestPromptFile_ConflictLintRule_FiresWhenBothSet verifies that when a node
// has both prompt_file and prompt/llm_prompt set and expandPromptFiles has NOT
// run (RepoPath is empty, so prompt_file remains unresolved), the
// prompt_file_conflict lint rule fires with severity ERROR.
//
// This exercises the standalone-validate path: lintPromptFileConflict checks
// for the ambiguous combination and reports it so the user knows.
func TestPromptFile_ConflictLintRule_FiresWhenBothSet(t *testing.T) {
	// Construct a minimal graph by hand so we can inject both attributes without
	// going through the DOT parser attribute-dedup semantics.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="inline text", prompt_file="prompts/some.md"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	diags := Validate(g)

	// prompt_file_conflict must fire as ERROR because both prompt and
	// prompt_file are set on node "a".
	assertHasRule(t, diags, "prompt_file_conflict", SeverityError)

	// The diagnostic must point at node "a".
	found := false
	for _, d := range diags {
		if d.Rule == "prompt_file_conflict" && d.NodeID == "a" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected prompt_file_conflict diagnostic for node 'a'; got %+v", diags)
	}
}

// TestPromptFile_ConflictLintRule_DoesNotFireWhenOnlyPromptFile verifies that
// a node with only prompt_file (and no prompt/llm_prompt) does not trigger
// the prompt_file_conflict lint rule, since there is no ambiguity.
func TestPromptFile_ConflictLintRule_DoesNotFireWhenOnlyPromptFile(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt_file="prompts/some.md"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	diags := Validate(g)

	// No conflict: only prompt_file is set, prompt is absent.
	assertNoRule(t, diags, "prompt_file_conflict")
}

// TestPromptFile_LlmPromptConflict_FiresWithLlmPromptAttr verifies that the
// prompt_file_conflict rule also fires when prompt_file conflicts with the
// legacy llm_prompt attribute (not just the primary prompt attribute).
func TestPromptFile_LlmPromptConflict_FiresWithLlmPromptAttr(t *testing.T) {
	// Build the graph directly via model to set llm_prompt without DOT
	// attribute normalization potentially merging it.
	g := model.NewGraph("G")
	start := model.NewNode("start")
	start.Attrs["shape"] = "Mdiamond"
	exit := model.NewNode("exit")
	exit.Attrs["shape"] = "Msquare"
	a := model.NewNode("a")
	a.Attrs["shape"] = "box"
	a.Attrs["llm_provider"] = "openai"
	a.Attrs["llm_model"] = "gpt-5.4"
	a.Attrs["llm_prompt"] = "legacy inline text"
	a.Attrs["prompt_file"] = "prompts/some.md"

	_ = g.AddNode(start)
	_ = g.AddNode(exit)
	_ = g.AddNode(a)
	g.Edges = append(g.Edges, &model.Edge{From: "start", To: "a"})
	g.Edges = append(g.Edges, &model.Edge{From: "a", To: "exit"})

	diags := Validate(g)

	// prompt_file_conflict must fire because llm_prompt + prompt_file are both set.
	assertHasRule(t, diags, "prompt_file_conflict", SeverityError)
	for _, d := range diags {
		if d.Rule == "prompt_file_conflict" && d.NodeID == "a" {
			return
		}
	}
	t.Fatalf("expected prompt_file_conflict for node 'a'; got %+v", diags)
}

// --- Tests for all_conditional_edges lint rule ---

func TestValidate_AllConditionalEdges_ErrorWhenAllConditional(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="y"]
  start -> a
  a -> b [condition="outcome=success"]
  a -> b [condition="outcome=fail"]
  b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintAllConditionalEdges(g)
	assertHasRule(t, diags, "all_conditional_edges", SeverityError)
}

func TestValidate_AllConditionalEdges_PassesWithUnconditionalFallback(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="y"]
  start -> a
  a -> b [condition="outcome=success"]
  a -> b [condition="outcome=fail"]
  a -> b
  b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintAllConditionalEdges(g)
	assertNoRule(t, diags, "all_conditional_edges")
}

// A complementary `outcome=X` / `outcome!=X` pair covers the full outcome
// space, so there is no routing gap even though every edge is conditional.
func TestValidate_AllConditionalEdges_ExemptOnExhaustivePair(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=parallelogram, tool_command="echo hi"]
  b [shape=parallelogram, tool_command="echo bye"]
  start -> a
  a -> b    [condition="outcome=success"]
  a -> exit [condition="outcome!=success"]
  b -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintAllConditionalEdges(g)
	assertNoRule(t, diags, "all_conditional_edges")
}

// When every outgoing edge from a node targets a terminal node, the engine
// terminates the run on a missed condition by intent — the routing-gap concern
// doesn't apply. terminal_condition_edge governs those edges instead.
func TestValidate_AllConditionalEdges_ExemptWhenAllTargetsTerminal(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="x"]
  start -> a
  a -> exit [condition="outcome=success"]
  a -> exit [condition="outcome=fail"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintAllConditionalEdges(g)
	assertNoRule(t, diags, "all_conditional_edges")
}

// --- Tests for goal_gate_has_retry lint rule ---

func TestValidate_GoalGateHasRetry_WarnWhenNoTargetAnywhere(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  review_consensus [shape=box, llm_provider=anthropic, llm_model="claude-sonnet-4.6", goal_gate=true, prompt="x"]
  start -> review_consensus -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintGoalGateHasRetry(g)
	assertHasRule(t, diags, "goal_gate_has_retry", SeverityWarning)
}

func TestValidate_GoalGateHasRetry_PassesWithGraphLevelTarget(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="implement"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  review_consensus [shape=box, llm_provider=anthropic, llm_model="claude-sonnet-4.6", goal_gate=true, prompt="x"]
  start -> review_consensus -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintGoalGateHasRetry(g)
	assertNoRule(t, diags, "goal_gate_has_retry")
}

// --- Tests for goal_gate_missing_node_retry_target lint rule ---

func TestValidate_GoalGateMissingNodeRetry_WarnWhenOnlyGraphLevel(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="implement"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  review_consensus [shape=box, llm_provider=anthropic, llm_model="claude-sonnet-4.6", goal_gate=true, prompt="x"]
  start -> review_consensus -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintGoalGateMissingNodeRetryTarget(g)
	assertHasRule(t, diags, "goal_gate_missing_node_retry_target", SeverityWarning)
}

func TestValidate_GoalGateMissingNodeRetry_PassesWithNodeLevel(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="implement"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  review_consensus [shape=box, llm_provider=anthropic, llm_model="claude-sonnet-4.6", goal_gate=true, retry_target="postmortem", prompt="x"]
  start -> review_consensus -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintGoalGateMissingNodeRetryTarget(g)
	assertNoRule(t, diags, "goal_gate_missing_node_retry_target")
}

// --- Tests for reserved_keyword_node_id lint rule ---

func TestValidate_ReservedKeywordNodeID_WarnsOnIfKeyword(t *testing.T) {
	// Build graph programmatically because DOT parser treats "if" as a keyword.
	g := model.NewGraph("G")
	n := model.NewNode("if")
	n.Attrs["shape"] = "box"
	n.Attrs["llm_provider"] = "openai"
	n.Attrs["llm_model"] = "gpt-5.4"
	_ = g.AddNode(n)

	diags := lintReservedKeywordNodeID(g)
	assertHasRule(t, diags, "reserved_keyword_node_id", SeverityWarning)
}

func TestValidate_ReservedKeywordNodeID_WarnsOnAllKeywords(t *testing.T) {
	keywords := []string{"graph", "digraph", "subgraph", "node", "edge", "strict", "if"}
	for _, kw := range keywords {
		t.Run(kw, func(t *testing.T) {
			g := model.NewGraph("G")
			n := model.NewNode(kw)
			n.Attrs["shape"] = "box"
			_ = g.AddNode(n)

			diags := lintReservedKeywordNodeID(g)
			assertHasRule(t, diags, "reserved_keyword_node_id", SeverityWarning)
		})
	}
}

func TestValidate_ReservedKeywordNodeID_PassesOnNormalID(t *testing.T) {
	g := model.NewGraph("G")
	n := model.NewNode("check_toolchain")
	n.Attrs["shape"] = "box"
	n.Attrs["llm_provider"] = "openai"
	n.Attrs["llm_model"] = "gpt-5.4"
	_ = g.AddNode(n)

	diags := lintReservedKeywordNodeID(g)
	assertNoRule(t, diags, "reserved_keyword_node_id")
}

// --- Tests for validate_script_failure_contract lint rule ---

func TestValidate_ValidateScriptFailureContract_MissingFallback_Warns(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  v [shape=parallelogram, tool_command="sh scripts/validate-build.sh"]
  start -> v -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "validate_script_failure_contract", SeverityWarning)
}

func TestValidate_ValidateScriptFailureContract_WithFallback_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  v [shape=parallelogram, tool_command="sh scripts/validate-build.sh || { echo 'KILROY_VALIDATE_FAILURE: build script missing'; exit 1; }"]
  start -> v -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "validate_script_failure_contract")
}

func TestValidate_ValidateScriptFailureContract_NonValidateScript_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  v [shape=parallelogram, tool_command="sh scripts/run-tests.sh"]
  start -> v -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "validate_script_failure_contract")
}

func TestValidate_ValidateScriptFailureContract_EchoCommand_NoWarn(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  v [shape=parallelogram, tool_command="echo ok"]
  start -> v -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "validate_script_failure_contract")
}

// --- status_outcome_field_confusion ---

func TestValidate_StatusOutcomeFieldConfusion_ErrorOnAntiPattern(t *testing.T) {
	// Prompt contains the antipattern: {"status":"success","outcome":"more_work"}
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  check [shape=box, prompt="Do work. Write status: echo '{\"status\":\"success\",\"outcome\":\"more_work\"}' > \"$KILROY_STAGE_STATUS_PATH\". Fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH."]
  start -> check -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "status_outcome_field_confusion", SeverityError)
}

func TestValidate_StatusOutcomeFieldConfusion_NoErrorOnCorrectPattern(t *testing.T) {
	// Correct pattern: {"status":"more_work"} with no extra outcome key
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  check [shape=box, prompt="Do work. Write status: echo '{\"status\":\"more_work\"}' > \"$KILROY_STAGE_STATUS_PATH\". Fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH."]
  start -> check -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_outcome_field_confusion")
}

func TestValidate_StatusOutcomeFieldConfusion_NoErrorOnNonBoxNode(t *testing.T) {
	// The antipattern in a non-box node should not trigger (no agent writes status there)
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "status_outcome_field_confusion")
}

// --- custom_outcome_coverage ---

func TestValidate_CustomOutcomeCoverage_WarnWhenMissing(t *testing.T) {
	// Edge requires outcome=more_work but prompt only writes status:success
	g, err := dot.Parse([]byte(`
digraph G {
  start      [shape=Mdiamond]
  exit       [shape=Msquare]
  work_pool  [shape=component]
  merger     [shape=box]
  check [shape=box, prompt="Harvest files. If all done: echo '{\"status\":\"success\"}' > \"$KILROY_STAGE_STATUS_PATH\". Fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH."]
  start -> check
  check -> work_pool [condition="outcome=more_work"]
  check -> merger
  work_pool -> check
  merger -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertHasRule(t, diags, "custom_outcome_coverage", SeverityWarning)
}

func TestValidate_CustomOutcomeCoverage_NoWarnWhenCovered(t *testing.T) {
	// Edge requires outcome=more_work and prompt correctly writes status:more_work
	g, err := dot.Parse([]byte(`
digraph G {
  start      [shape=Mdiamond]
  exit       [shape=Msquare]
  work_pool  [shape=component]
  merger     [shape=box]
  check [shape=box, prompt="Harvest files. If done: echo '{\"status\":\"success\"}' > \"$KILROY_STAGE_STATUS_PATH\". If more work: echo '{\"status\":\"more_work\"}' > \"$KILROY_STAGE_STATUS_PATH\". Fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH."]
  start -> check
  check -> work_pool [condition="outcome=more_work"]
  check -> merger
  work_pool -> check
  merger -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "custom_outcome_coverage")
}

func TestValidate_CustomOutcomeCoverage_NoWarnForCanonicalOutcomes(t *testing.T) {
	// condition="outcome=success" is canonical — no coverage check needed
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  check [shape=box, prompt="Do work. Write: echo '{\"status\":\"success\"}' > \"$KILROY_STAGE_STATUS_PATH\". Fallback: $KILROY_STAGE_STATUS_FALLBACK_PATH."]
  bad   [shape=box]
  start -> check
  check -> exit   [condition="outcome=success"]
  check -> bad
  bad -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g)
	assertNoRule(t, diags, "custom_outcome_coverage")
}
