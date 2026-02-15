package engine

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestSelectNextEdge_ConditionBeatsUnconditionalWeight(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=success", weight=0]
  a -> c [weight=100]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e == nil || e.To != "b" {
		t.Fatalf("edge: got %+v want to=b", e)
	}
}

func TestSelectNextEdge_PreferredLabelBeatsWeightAmongUnconditional(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [label="[A] Approve", weight=0]
  a -> c [label="[F] Fix", weight=100]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess, PreferredLabel: "Approve"}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e == nil || e.To != "b" {
		t.Fatalf("edge: got %+v want to=b", e)
	}
}

func TestSelectNextEdge_SuggestedNextIDsBeatsWeightAmongUnconditional(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [weight=100]
  a -> c [weight=0]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess, SuggestedNextIDs: []string{"c"}}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e == nil || e.To != "c" {
		t.Fatalf("edge: got %+v want to=c", e)
	}
}

func TestSelectNextEdge_WeightThenLexicalThenOrder(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  d [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> d [weight=2]
  a -> c [weight=2]
  a -> b [weight=2]
  b -> exit
  c -> exit
  d -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	// All weights tied; lexical by to_node chooses "b".
	if e == nil || e.To != "b" {
		t.Fatalf("edge: got %+v want to=b", e)
	}
}

func TestSelectAllEligibleEdges_MultipleUnconditional(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  d [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b
  a -> c
  a -> d
  b -> exit
  c -> exit
  d -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 3 {
		t.Fatalf("got %d edges, want 3", len(edges))
	}
	targets := map[string]bool{}
	for _, e := range edges {
		targets[e.To] = true
	}
	for _, want := range []string{"b", "c", "d"} {
		if !targets[want] {
			t.Fatalf("missing target %q", want)
		}
	}
}

func TestSelectAllEligibleEdges_MultipleMatchingConditions(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=diamond]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  d [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=success"]
  a -> c [condition="outcome=success"]
  a -> d [condition="outcome=fail"]
  b -> exit
  c -> exit
  d -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("got %d edges, want 2 (b and c)", len(edges))
	}
}

func TestSelectAllEligibleEdges_SingleEdge(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b
  b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	if edges[0].To != "b" {
		t.Fatalf("got %q, want b", edges[0].To)
	}
}

func TestSelectAllEligibleEdges_PreferredLabelNarrowsToOne(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [label="approve"]
  a -> c [label="reject"]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess, PreferredLabel: "approve"}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (preferred label narrows)", len(edges))
	}
	if edges[0].To != "b" {
		t.Fatalf("got %q, want b", edges[0].To)
	}
}

// --- V3.2: No eligible edge when all conditions fail (no fallback) ---

func TestSelectAllEligibleEdges_NoEligibleEdge_AllConditionsFailed(t *testing.T) {
	// When all edges have conditions and none match, return nil (routing gap).
	// The user should add an unconditional fallback edge.
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=diamond]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=success"]
  a -> c [condition="outcome=fail"]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Outcome is partial_success -- neither "outcome=success" nor "outcome=fail" matches.
	out := runtime.Outcome{Status: runtime.StatusPartialSuccess}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	// No fallback — routing gap returns nil.
	if edges != nil {
		t.Fatalf("got %d edges, want nil (no eligible edge — routing gap)", len(edges))
	}
}

func TestSelectNextEdge_NoEligibleEdge_ReturnsNil(t *testing.T) {
	// When all conditions fail and no unconditional edge exists, selectNextEdge
	// returns nil — the graph has a routing gap.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=diamond]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> c [condition="outcome=success", weight=10]
  a -> b [condition="outcome=fail", weight=5]
  c -> exit
  b -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// partial_success matches neither condition. No unconditional edge exists.
	out := runtime.Outcome{Status: runtime.StatusPartialSuccess}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e != nil {
		t.Fatalf("expected nil edge (routing gap), got to=%s", e.To)
	}
}

// --- V3.3: Preferred label searches ALL edges, not just unconditional ---

func TestSelectNextEdge_PreferredLabelMatchesConditionalEdge(t *testing.T) {
	// V3.3: Preferred label match (Step 2) iterates ALL edges per spec section 3.3.
	// A conditional edge whose condition did not pass but whose label matches
	// should still be selected by preferred label.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=fail", label="[A] Approve", weight=0]
  a -> c [condition="outcome=fail", label="[R] Reject", weight=100]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Outcome is success -- neither condition matches (both require outcome=fail).
	// But preferred_label="Approve" matches edge a->b's label.
	out := runtime.Outcome{Status: runtime.StatusSuccess, PreferredLabel: "Approve"}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e == nil || e.To != "b" {
		t.Fatalf("edge: got %+v want to=b (preferred label match on conditional edge)", e)
	}
}

func TestSelectAllEligibleEdges_PreferredLabelSearchesAllEdges(t *testing.T) {
	// V3.3: When no condition matches and there are only conditional edges,
	// preferred label should still find a match among them.
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=fail", label="approve"]
  a -> c [condition="outcome=fail", label="reject"]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess, PreferredLabel: "reject"}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (preferred label narrows to one)", len(edges))
	}
	if edges[0].To != "c" {
		t.Fatalf("got %q, want c (preferred label match)", edges[0].To)
	}
}

// --- V3.4: Suggested next IDs searches ALL edges, not just unconditional ---

func TestSelectNextEdge_SuggestedNextIDMatchesConditionalEdge(t *testing.T) {
	// V3.4: Suggested next IDs (Step 3) iterates ALL edges per spec section 3.3.
	// A conditional edge whose condition did not pass but whose target matches
	// a suggested ID should still be selected.
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=fail", weight=100]
  a -> c [condition="outcome=fail", weight=0]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Outcome is success -- neither condition matches. SuggestedNextIDs picks "c".
	out := runtime.Outcome{Status: runtime.StatusSuccess, SuggestedNextIDs: []string{"c"}}
	ctx := runtime.NewContext()
	e, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	if e == nil || e.To != "c" {
		t.Fatalf("edge: got %+v want to=c (suggested next ID match on conditional edge)", e)
	}
}

func TestSelectAllEligibleEdges_SuggestedNextIDSearchesAllEdges(t *testing.T) {
	// V3.4: When no condition matches and there are only conditional edges,
	// suggested next IDs should still find a match among them.
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> b [condition="outcome=fail"]
  a -> c [condition="outcome=fail"]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusSuccess, SuggestedNextIDs: []string{"c"}}
	ctx := runtime.NewContext()
	edges, err := selectAllEligibleEdges(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectAllEligibleEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1 (suggested ID narrows to one)", len(edges))
	}
	if edges[0].To != "c" {
		t.Fatalf("got %q, want c (suggested next ID match)", edges[0].To)
	}
}
