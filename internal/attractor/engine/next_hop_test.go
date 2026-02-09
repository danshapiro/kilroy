package engine

import (
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/dot"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestResolveNextHop_FanInFail_DoesNotPickUnconditionalEdge(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  join [shape=tripleoctagon]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext())
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
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext())
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
  retry_by_condition [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2]
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
	}, runtime.NewContext())
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
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "all parallel branches failed",
	}, runtime.NewContext(), failureClassDeterministic)
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
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_node [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  retry_global [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  join -> verify
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	hop, err := resolveNextHop(g, "join", runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "upstream timeout",
	}, runtime.NewContext(), failureClassTransientInfra)
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

func TestResolveNextHop_NonFanIn_PreservesSelectNextEdgeBehavior(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
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
	got, err := resolveNextHop(g, "a", out, ctx)
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
