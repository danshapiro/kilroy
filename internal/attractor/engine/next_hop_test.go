package engine

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestResolveNextHop_FanInFail_DoesNotPickUnconditionalEdge(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  join [shape=tripleoctagon]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), "", nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop != nil {
		t.Fatalf("expected nil next hop for fan-in fail with no fail condition/retry_target, got %+v", hop)
	}
}

func TestResolveNextHop_FanInFail_PicksRetryTarget(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="retry_global"]
  join [shape=tripleoctagon, retry_target="retry_node"]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), failureClassTransientInfra, nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop == nil || hop.Edge == nil {
		t.Fatalf("expected retry target hop, got %+v", hop)
	}
	if hop.Edge.To != "retry_node" {
		t.Fatalf("next hop target: got %q want %q", hop.Edge.To, "retry_node")
	}
	if hop.Source != nextHopSourceRetryTarget {
		t.Fatalf("hop source: got %q want %q", hop.Source, nextHopSourceRetryTarget)
	}
	if hop.RetryTargetSource != "node.retry_target" {
		t.Fatalf("retry target source: got %q want %q", hop.RetryTargetSource, "node.retry_target")
	}
}

func TestResolveNextHop_FanInFail_ConditionalBeatsRetryTarget(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  join [shape=tripleoctagon, retry_target="retry_node"]
  retry_by_condition [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  join -> retry_by_condition [condition="outcome=fail"]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), "", nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop == nil || hop.Edge == nil {
		t.Fatalf("expected conditional hop, got %+v", hop)
	}
	if hop.Edge.To != "retry_by_condition" {
		t.Fatalf("next hop target: got %q want %q", hop.Edge.To, "retry_by_condition")
	}
	if hop.Source != nextHopSourceConditional {
		t.Fatalf("hop source: got %q want %q", hop.Source, nextHopSourceConditional)
	}
}

func TestResolveNextHop_FanInFail_DeterministicBlocksRetryTarget(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="retry_global"]
  join [shape=tripleoctagon, retry_target="retry_node"]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), failureClassDeterministic, nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop != nil {
		t.Fatalf("expected nil hop for deterministic fan-in failure with retry_target, got edge to %q (source=%s)", hop.Edge.To, hop.Source)
	}
}

func TestResolveNextHop_FanInFail_TransientAllowsRetryTarget(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="retry_global"]
  join [shape=tripleoctagon, retry_target="retry_node"]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "upstream timeout",
	}, runtime.NewContext(), failureClassTransientInfra, nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop == nil || hop.Edge == nil {
		t.Fatalf("expected retry target hop for transient fan-in failure, got nil")
	}
	if hop.Edge.To != "retry_node" {
		t.Fatalf("next hop target: got %q want %q", hop.Edge.To, "retry_node")
	}
	if hop.Source != nextHopSourceRetryTarget {
		t.Fatalf("hop source: got %q want %q", hop.Source, nextHopSourceRetryTarget)
	}
}

// TestRetryTargetPrecedenceChain exercises the full four-level resolution
// hierarchy implemented in resolveRetryTargetWithSource:
//
//	node.retry_target
//	  > node.fallback_retry_target
//	  > graph.retry_target
//	  > graph.fallback_retry_target
//
// Each case asserts both the resolved target and the source label.
func TestRetryTargetPrecedenceChain(t *testing.T) {
	// buildGraph constructs a minimal Graph with a tripleoctagon fan-in node
	// ("join") whose attributes are supplied by nodeAttrs, and graph-level
	// attributes supplied by graphAttrs.
	buildGraph := func(graphAttrs, nodeAttrs map[string]string) *model.Graph {
		g := model.NewGraph("G")
		for k, v := range graphAttrs {
			g.Attrs[k] = v
		}
		join := model.NewNode("join")
		join.Attrs["shape"] = "tripleoctagon"
		for k, v := range nodeAttrs {
			join.Attrs[k] = v
		}
		_ = g.AddNode(join)
		return g
	}

	cases := []struct {
		name       string
		graphAttrs map[string]string
		nodeAttrs  map[string]string
		wantTarget string
		wantSource string
	}{
		{
			name:       "only graph.fallback_retry_target set",
			graphAttrs: map[string]string{"fallback_retry_target": "graph_fallback"},
			nodeAttrs:  map[string]string{},
			wantTarget: "graph_fallback",
			wantSource: "graph.fallback_retry_target",
		},
		{
			name: "graph.retry_target overrides graph.fallback_retry_target",
			graphAttrs: map[string]string{
				"retry_target":          "graph_primary",
				"fallback_retry_target": "graph_fallback",
			},
			nodeAttrs:  map[string]string{},
			wantTarget: "graph_primary",
			wantSource: "graph.retry_target",
		},
		{
			name: "node.fallback_retry_target overrides both graph-level attrs",
			graphAttrs: map[string]string{
				"retry_target":          "graph_primary",
				"fallback_retry_target": "graph_fallback",
			},
			nodeAttrs:  map[string]string{"fallback_retry_target": "node_fallback"},
			wantTarget: "node_fallback",
			wantSource: "node.fallback_retry_target",
		},
		{
			name: "node.retry_target overrides node.fallback_retry_target and both graph-level attrs",
			graphAttrs: map[string]string{
				"retry_target":          "graph_primary",
				"fallback_retry_target": "graph_fallback",
			},
			nodeAttrs: map[string]string{
				"retry_target":          "node_primary",
				"fallback_retry_target": "node_fallback",
			},
			wantTarget: "node_primary",
			wantSource: "node.retry_target",
		},
		{
			name:       "node.fallback_retry_target — no graph-level attrs",
			graphAttrs: map[string]string{},
			nodeAttrs: map[string]string{
				"fallback_retry_target": "node_fallback",
			},
			wantTarget: "node_fallback",
			wantSource: "node.fallback_retry_target",
		},
		{
			name:       "none set — empty result",
			graphAttrs: map[string]string{},
			nodeAttrs:  map[string]string{},
			wantTarget: "",
			wantSource: "",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			g := buildGraph(tc.graphAttrs, tc.nodeAttrs)
			gotTarget, gotSource := resolveRetryTargetWithSource(g, "join")
			if gotTarget != tc.wantTarget {
				t.Errorf("target: got %q want %q", gotTarget, tc.wantTarget)
			}
			if gotSource != tc.wantSource {
				t.Errorf("source: got %q want %q", gotSource, tc.wantSource)
			}
		})
	}
}

// TestRetryTargetPrecedenceChain_DeterministicOverride verifies that even when
// node.retry_target is set, a deterministic-class fan-in failure causes
// resolveNextHop to return nil (the retry_target hierarchy is bypassed entirely).
func TestRetryTargetPrecedenceChain_DeterministicOverride(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [retry_target="graph_primary", fallback_retry_target="graph_fallback"]
  join [shape=tripleoctagon, retry_target="node_primary", fallback_retry_target="node_fallback"]
  verify [shape=box, llm_provider=openai, llm_model=gpt-4]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), failureClassDeterministic, nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if hop != nil {
		t.Fatalf("expected nil hop for deterministic fan-in failure with all four retry attrs set, got edge to %q (source=%s, retry_target_source=%s)",
			hop.Edge.To, hop.Source, hop.RetryTargetSource)
	}
}

func TestResolveNextHop_NonFanIn_PreservesSelectNextEdgeBehavior(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.4]
  a -> b [weight=10]
  a -> c [weight=1]
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	out := runtime.Outcome{Status: runtime.StatusFail, FailureReason: "boom"}
	ctx := runtime.NewContext()

	want, err := selectNextEdge(g, "a", out, ctx)
	if err != nil {
		t.Fatalf("selectNextEdge: %v", err)
	}
	got, err := resolveNextHop(g, "a", out, ctx, "", nil)
	if err != nil {
		t.Fatalf("resolveNextHop: %v", err)
	}
	if want == nil && got != nil {
		t.Fatalf("expected nil hop, got %+v", got)
	}
	if want != nil {
		if got == nil || got.Edge == nil {
			t.Fatalf("expected hop edge, got %+v", got)
		}
		if got.Edge.To != want.To {
			t.Fatalf("next hop target: got %q want %q", got.Edge.To, want.To)
		}
	}
	if got != nil && got.Source != nextHopSourceEdgeSelection {
		t.Fatalf("hop source: got %q want %q", got.Source, nextHopSourceEdgeSelection)
	}
}
