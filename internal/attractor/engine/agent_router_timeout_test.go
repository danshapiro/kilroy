package engine

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
)

func TestResolveAgentLoopCommandTimeouts_NodeAttrsOverrideGraph(t *testing.T) {
	g := model.NewGraph("g")
	g.Attrs["default_command_timeout_ms"] = "60000"
	g.Attrs["max_command_timeout_ms"] = "600000"
	node := model.NewNode("n")
	node.Attrs["default_command_timeout_ms"] = "120000"
	node.Attrs["max_command_timeout_ms"] = "900000"

	gotDefault, gotMax := resolveAgentLoopCommandTimeouts(&Execution{Graph: g}, node)
	if gotDefault != 120000 {
		t.Fatalf("default timeout=%d want 120000", gotDefault)
	}
	if gotMax != 900000 {
		t.Fatalf("max timeout=%d want 900000", gotMax)
	}
}

func TestResolveAgentLoopCommandTimeouts_FallsBackToGraphAttrs(t *testing.T) {
	g := model.NewGraph("g")
	g.Attrs["default_command_timeout_ms"] = "60000"
	g.Attrs["max_command_timeout_ms"] = "600000"
	node := model.NewNode("n")

	gotDefault, gotMax := resolveAgentLoopCommandTimeouts(&Execution{Graph: g}, node)
	if gotDefault != 60000 {
		t.Fatalf("default timeout=%d want 60000", gotDefault)
	}
	if gotMax != 600000 {
		t.Fatalf("max timeout=%d want 600000", gotMax)
	}
}

func TestResolveAgentLoopCommandTimeouts_IgnoresNonPositiveNodeValues(t *testing.T) {
	g := model.NewGraph("g")
	g.Attrs["default_command_timeout_ms"] = "60000"
	g.Attrs["max_command_timeout_ms"] = "600000"
	node := model.NewNode("n")
	node.Attrs["default_command_timeout_ms"] = "0"
	node.Attrs["max_command_timeout_ms"] = "-1"

	gotDefault, gotMax := resolveAgentLoopCommandTimeouts(&Execution{Graph: g}, node)
	if gotDefault != 60000 {
		t.Fatalf("default timeout=%d want 60000", gotDefault)
	}
	if gotMax != 600000 {
		t.Fatalf("max timeout=%d want 600000", gotMax)
	}
}
