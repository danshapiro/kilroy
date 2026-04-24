package validate

import (
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
)

// TestTerminalConditionEdge_FalliblePredecessor_NoCondition verifies that the
// rule fires when a fallible (agent or tool) node has an unconditional edge to
// a terminal node — the bug class described in fix #3.
func TestTerminalConditionEdge_FalliblePredecessor_NoCondition(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  agent [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4.6,
         prompt="Do work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> agent -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertHasRule(t, diags, "terminal_condition_edge", SeverityError)
}

// TestTerminalConditionEdge_FallibleToolPredecessor_NoCondition verifies that
// the rule fires for tool (parallelogram) nodes as well as agent (box) nodes.
func TestTerminalConditionEdge_FallibleToolPredecessor_NoCondition(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  setup [shape=parallelogram, tool_command="sh setup.sh"]
  start -> setup -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertHasRule(t, diags, "terminal_condition_edge", SeverityError)
}

// TestTerminalConditionEdge_ErrorMessageContainsNodeIDs verifies that the
// diagnostic message includes both the source and target node IDs so the graph
// author can locate the offending edge (acceptance criterion #2).
func TestTerminalConditionEdge_ErrorMessageContainsNodeIDs(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start  [shape=Mdiamond]
  done   [shape=Msquare]
  worker [shape=box, llm_provider=openai, llm_model=gpt-5.4,
          prompt="Do work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> worker -> done
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)

	var found *Diagnostic
	for i := range diags {
		if diags[i].Rule == "terminal_condition_edge" {
			found = &diags[i]
			break
		}
	}
	if found == nil {
		t.Fatal("expected terminal_condition_edge diagnostic, got none")
	}
	if found.EdgeFrom == "" {
		t.Error("terminal_condition_edge diagnostic must include EdgeFrom")
	}
	if found.EdgeTo == "" {
		t.Error("terminal_condition_edge diagnostic must include EdgeTo")
	}
	if !strings.Contains(found.Message, "worker") {
		t.Errorf("expected message to contain source node id %q; got: %s", "worker", found.Message)
	}
	if !strings.Contains(found.Message, "done") {
		t.Errorf("expected message to contain target node id %q; got: %s", "done", found.Message)
	}
}

// TestTerminalConditionEdge_SuccessOnlyStart_NoConditionAllowed verifies that
// an unconditional edge from a start node to a terminal does NOT trigger the
// rule (acceptance criterion #3: "start -> exit is still valid").
func TestTerminalConditionEdge_SuccessOnlyStart_NoConditionAllowed(t *testing.T) {
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
	diags := lintTerminalConditionEdge(g)
	assertNoRule(t, diags, "terminal_condition_edge")
}

// TestTerminalConditionEdge_WithCondition_NoError verifies that a fallible
// predecessor whose edge to the terminal carries an explicit condition passes
// (acceptance criterion #4: "agent -> exit [condition=...] passes").
func TestTerminalConditionEdge_WithCondition_NoError(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  agent [shape=box, llm_provider=anthropic, llm_model=claude-sonnet-4.6,
         prompt="Do work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> agent
  agent -> exit [condition="outcome=success"]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertNoRule(t, diags, "terminal_condition_edge")
}

// TestTerminalConditionEdge_AgentWithoutCondition_Error is the canonical example
// from acceptance criterion #5: "start -> agent -> exit without condition FAILS".
func TestTerminalConditionEdge_AgentWithoutCondition_Error(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  agent [shape=box, llm_provider=openai, llm_model=gpt-5.4,
         prompt="Do work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> agent -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertHasRule(t, diags, "terminal_condition_edge", SeverityError)
}

// TestTerminalConditionEdge_ConditionalPredecessor_Exempt verifies that a
// diamond (conditional) node is treated as success-only: no condition required
// on its outgoing edge to a terminal.
func TestTerminalConditionEdge_ConditionalPredecessor_Exempt(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  agent  [shape=box, llm_provider=openai, llm_model=gpt-5.4,
          prompt="Do work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  router [shape=diamond]
  start -> agent -> router
  router -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	// The router -> exit edge has no condition but router is a diamond (success-only).
	assertNoRule(t, diags, "terminal_condition_edge")
}

// TestTerminalConditionEdge_LoopBeginAndEnd_Exempt verifies that loop.begin
// (trapezium) and loop.end (invtrapezium) are treated as success-only.
func TestTerminalConditionEdge_LoopBeginAndEnd_Exempt(t *testing.T) {
	// Loop end routing to terminal with no condition should be allowed because
	// loop.end is a deterministic sentinel.
	g, err := dot.Parse([]byte(`
digraph G {
  start      [shape=Mdiamond]
  exit       [shape=Msquare]
  loop_begin [shape=trapezium, loop_id=main, loop_max=3]
  loop_end   [shape=invtrapezium, loop_id=main, loop_max=3]
  start -> loop_begin -> loop_end -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertNoRule(t, diags, "terminal_condition_edge")
}

// TestTerminalConditionEdge_DoublecircleTerminal_AlsoCaught verifies that the
// rule applies to doublecircle-shaped terminal nodes, not just Msquare.
func TestTerminalConditionEdge_DoublecircleTerminal_AlsoCaught(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  done  [shape=doublecircle]
  agent [shape=box, llm_provider=openai, llm_model=gpt-5.4,
         prompt="Work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> agent -> done
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	assertHasRule(t, diags, "terminal_condition_edge", SeverityError)
}

// TestTerminalConditionEdge_IntegratedInValidate verifies that the rule is
// wired into the top-level Validate function (not just the lint helper).
func TestTerminalConditionEdge_IntegratedInValidate(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  agent [shape=box, llm_provider=openai, llm_model=gpt-5.4,
         prompt="Work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  start -> agent -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := Validate(g) // top-level: should include terminal_condition_edge
	assertHasRule(t, diags, "terminal_condition_edge", SeverityError)
}

// TestTerminalConditionEdge_MultipleOffendingEdges verifies that every
// unconditional inbound-to-terminal edge from a fallible predecessor is reported
// (not just the first one found).
func TestTerminalConditionEdge_MultipleOffendingEdges(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  step_a [shape=box, llm_provider=openai, llm_model=gpt-5.4,
          prompt="Work. Write status to $KILROY_STAGE_STATUS_PATH or $KILROY_STAGE_STATUS_FALLBACK_PATH if that fails."]
  step_b [shape=parallelogram, tool_command="sh b.sh"]
  start -> step_a -> step_b
  step_a -> exit
  step_b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	diags := lintTerminalConditionEdge(g)
	count := 0
	for _, d := range diags {
		if d.Rule == "terminal_condition_edge" {
			count++
		}
	}
	if count < 2 {
		t.Fatalf("expected at least 2 terminal_condition_edge diagnostics (one per offending edge); got %d", count)
	}
}
