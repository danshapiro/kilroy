package dot

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

func TestParse_SimpleChainedEdgesAndDefaults(t *testing.T) {
	src := []byte(`
// comment
digraph Simple {
    graph [goal="Run tests and report"]
    rankdir=LR

    node [shape=box, timeout=900s]
    edge [weight=0]

    start [shape=Mdiamond, label="Start"]
    exit  [shape=Msquare, label="Exit"]

    run_tests [label="Run Tests", prompt="Run the test suite and report results"]
    report    [label="Report", prompt="Summarize the test results"]

    start -> run_tests -> report -> exit
}
`)
	g, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if g.Name != "Simple" {
		t.Fatalf("graph name: got %q", g.Name)
	}
	if got := g.Attrs["goal"]; got != "Run tests and report" {
		t.Fatalf("graph goal: got %q", got)
	}
	if got := g.Attrs["rankdir"]; got != "LR" {
		t.Fatalf("rankdir: got %q", got)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes: got %d", len(g.Nodes))
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges: got %d", len(g.Edges))
	}
	// Default node attrs should apply.
	if g.Nodes["run_tests"].Attr("timeout", "") != "900s" {
		t.Fatalf("timeout default not applied: %q", g.Nodes["run_tests"].Attr("timeout", ""))
	}
	// Explicit node attrs override defaults.
	if g.Nodes["start"].Shape() != "Mdiamond" {
		t.Fatalf("start shape: got %q", g.Nodes["start"].Shape())
	}
}

func TestParse_MultilineAttrsAndComments(t *testing.T) {
	src := []byte(`
digraph X {
    /* block comment with -> and [ ] */
    start [shape=Mdiamond]
    node1 [
        label="Node 1",
        prompt="line1\nline2",
        tool_hooks.pre="echo hi"
    ]
    // trailing comment
    exit [shape=Msquare]
    start -> node1 -> exit
}
`)
	g, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	n := g.Nodes["node1"]
	if n == nil {
		t.Fatalf("node1 missing")
	}
	if n.Attr("label", "") != "Node 1" {
		t.Fatalf("label: got %q", n.Attr("label", ""))
	}
	if n.Attr("prompt", "") != "line1\nline2" {
		t.Fatalf("prompt: got %q", n.Attr("prompt", ""))
	}
	if n.Attr("tool_hooks.pre", "") != "echo hi" {
		t.Fatalf("qualified key parse: got %q", n.Attr("tool_hooks.pre", ""))
	}
}

func TestParse_SubgraphLabelDerivesClass(t *testing.T) {
	src := []byte(`
digraph G {
    start [shape=Mdiamond]
    exit [shape=Msquare]

    subgraph cluster_loop {
        label="Loop A"
        node [thread_id="loop-a"]
        Plan      [label="Plan next step"]
        Implement [label="Implement"]
    }

    start -> Plan -> Implement -> exit
}
`)
	g, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	for _, id := range []string{"Plan", "Implement"} {
		n := g.Nodes[id]
		if n == nil {
			t.Fatalf("%s missing", id)
		}
		if n.Attr("thread_id", "") != "loop-a" {
			t.Fatalf("%s thread_id: got %q", id, n.Attr("thread_id", ""))
		}
		classes := n.ClassList()
		if !contains(classes, "loop-a") {
			t.Fatalf("%s classes: got %#v", id, classes)
		}
	}
}

func TestParse_TopLevelNegativeNumericValue(t *testing.T) {
	src := []byte(`
digraph G {
    some_threshold = -1
    float_val = -3.5
    start [shape=Mdiamond]
    exit  [shape=Msquare]
    start -> exit
}
`)
	g, err := Parse(src)
	if err != nil {
		t.Fatalf("Parse() error: %v", err)
	}
	if got := g.Attrs["some_threshold"]; got != "-1" {
		t.Fatalf("some_threshold: got %q, want %q", got, "-1")
	}
	if got := g.Attrs["float_val"]; got != "-3.5" {
		t.Fatalf("float_val: got %q, want %q", got, "-3.5")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

var _ = model.Graph{} // keep the import honest as the package evolves
