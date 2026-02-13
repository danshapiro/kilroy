package engine

import (
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/dot"
	"github.com/strongdm/kilroy/internal/attractor/model"
)

func TestFindJoinNode_PrefersTripleoctagon(t *testing.T) {
	// When both a tripleoctagon and a box convergence exist, prefer tripleoctagon.
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  join [shape=tripleoctagon]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  start -> b
  a -> join
  b -> join
  join -> synth
  synth -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	branches := g.Outgoing("start")
	var branchEdges []*model.Edge
	for _, e := range branches {
		if e != nil {
			branchEdges = append(branchEdges, e)
		}
	}
	joinID, err := findJoinNode(g, branchEdges)
	if err != nil {
		t.Fatalf("findJoinNode: %v", err)
	}
	if joinID != "join" {
		t.Fatalf("got %q, want join (tripleoctagon preferred)", joinID)
	}
}

func TestFindJoinNode_FallsBackToBoxConvergence(t *testing.T) {
	// When no tripleoctagon exists, find the first box convergence node.
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  start -> b
  start -> c
  a -> synth
  b -> synth
  c -> synth
  synth -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	branches := g.Outgoing("start")
	joinID, err := findJoinNode(g, branches)
	if err != nil {
		t.Fatalf("findJoinNode: %v", err)
	}
	if joinID != "synth" {
		t.Fatalf("got %q, want synth (box convergence fallback)", joinID)
	}
}

func TestFindJoinNode_NoBranches_Error(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  start -> exit
}
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	_, err = findJoinNode(g, nil)
	if err == nil {
		t.Fatal("expected error for nil branches")
	}
}
