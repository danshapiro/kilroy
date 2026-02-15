package engine

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestParseManagerActions(t *testing.T) {
	tests := []struct {
		input string
		want  map[string]bool
	}{
		{"observe,wait", map[string]bool{"observe": true, "wait": true}},
		{"observe,steer,wait", map[string]bool{"observe": true, "steer": true, "wait": true}},
		{"observe", map[string]bool{"observe": true}},
		{"", map[string]bool{}},
		{"  observe , wait  ", map[string]bool{"observe": true, "wait": true}},
		{"OBSERVE,WAIT", map[string]bool{"observe": true, "wait": true}},
	}
	for _, tc := range tests {
		got := parseManagerActions(tc.input)
		if len(got) != len(tc.want) {
			t.Errorf("parseManagerActions(%q) = %v, want %v", tc.input, got, tc.want)
			continue
		}
		for k := range tc.want {
			if !got[k] {
				t.Errorf("parseManagerActions(%q) missing key %q", tc.input, k)
			}
		}
	}
}

func TestManagerLoop_MaxCycles_ReturnsFail(t *testing.T) {
	node := &model.Node{
		ID: "manager1",
		Attrs: map[string]string{
			"manager.max_cycles":    "3",
			"manager.poll_interval": "1ms",
			"manager.actions":       "wait",
			"stack.child_autostart": "false",
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: runtime.NewContext(),
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     eng.Context,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	ctx := context.Background()
	out, err := h.Execute(ctx, exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
	if out.FailureReason == "" {
		t.Error("expected non-empty failure reason")
	}
}

func TestManagerLoop_StopCondition_ReturnsSuccess(t *testing.T) {
	node := &model.Node{
		ID: "manager2",
		Attrs: map[string]string{
			"manager.max_cycles":      "100",
			"manager.poll_interval":   "1ms",
			"manager.actions":         "wait",
			"manager.stop_condition":  "context.should_stop = yes",
			"stack.child_autostart":   "false",
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	rctx := runtime.NewContext()
	// Set the stop condition value before execution starts.
	rctx.Set("should_stop", "yes")

	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: rctx,
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     rctx,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	ctx := context.Background()
	out, err := h.Execute(ctx, exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s (reason: %s)", out.Status, out.FailureReason)
	}
}

func TestManagerLoop_StopConditionNotMet_CyclesExhaust(t *testing.T) {
	node := &model.Node{
		ID: "manager3",
		Attrs: map[string]string{
			"manager.max_cycles":      "5",
			"manager.poll_interval":   "1ms",
			"manager.actions":         "wait",
			"manager.stop_condition":  "context.should_stop = yes",
			"stack.child_autostart":   "false",
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	rctx := runtime.NewContext()
	// Don't set the stop condition value -- it will never be true.

	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: rctx,
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     rctx,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	ctx := context.Background()
	out, err := h.Execute(ctx, exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL (max cycles), got %s", out.Status)
	}
}

func TestManagerLoop_ContextCancellation(t *testing.T) {
	node := &model.Node{
		ID: "manager4",
		Attrs: map[string]string{
			"manager.max_cycles":    "10000",
			"manager.poll_interval": "100ms",
			"manager.actions":       "wait",
			"stack.child_autostart": "false",
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: runtime.NewContext(),
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     eng.Context,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	out, err := h.Execute(ctx, exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL (canceled), got %s", out.Status)
	}
}

func TestManagerLoop_DefaultAttributes(t *testing.T) {
	node := &model.Node{
		ID:    "manager5",
		Attrs: map[string]string{},
	}
	// Verify that the handler doesn't crash with all defaults.
	// With max_cycles=1000 and poll_interval=45s, this would take forever,
	// so we set max_cycles=1 to exercise the defaults path quickly.
	// child_autostart=false avoids the fast-fail validation for missing child_dotfile.
	node.Attrs["manager.max_cycles"] = "1"
	node.Attrs["manager.poll_interval"] = "1ms"
	node.Attrs["stack.child_autostart"] = "false"

	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: runtime.NewContext(),
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     eng.Context,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	out, err := h.Execute(context.Background(), exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With max_cycles=1 and no child/stop condition, should exhaust cycles.
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
}

func TestManagerLoop_AutostartWithoutDotfile_FailsFast(t *testing.T) {
	// When child_autostart defaults to true but child_dotfile is missing,
	// the handler should fail immediately with an actionable error rather
	// than silently entering the observation loop (which would run for
	// ~12.5 hours with default settings before reporting max-cycles exceeded).
	node := &model.Node{
		ID: "manager-misconfig",
		Attrs: map[string]string{
			"manager.max_cycles":    "1000",
			"manager.poll_interval": "45s",
			// stack.child_autostart defaults to "true" (not set = true)
			// stack.child_dotfile is intentionally absent
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{}, // no graph-level child_dotfile either
	}
	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: runtime.NewContext(),
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     eng.Context,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	start := time.Now()
	out, err := h.Execute(context.Background(), exec, node)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
	if out.FailureReason == "" || !strings.Contains(out.FailureReason, "child_dotfile") {
		t.Errorf("expected failure reason mentioning child_dotfile, got: %s", out.FailureReason)
	}
	// Should return nearly instantly, not enter the observation loop.
	if elapsed > 1*time.Second {
		t.Errorf("expected fast failure, took %v", elapsed)
	}
}

func TestManagerLoop_AutostartFalseWithoutDotfile_DoesNotFailFast(t *testing.T) {
	// When child_autostart is explicitly false, missing child_dotfile
	// should NOT trigger the fast-fail — the loop runs normally.
	node := &model.Node{
		ID: "manager-no-autostart",
		Attrs: map[string]string{
			"manager.max_cycles":     "2",
			"manager.poll_interval":  "1ms",
			"manager.actions":        "wait",
			"stack.child_autostart":  "false",
			// stack.child_dotfile intentionally absent
		},
	}
	graph := &model.Graph{
		Nodes: map[string]*model.Node{node.ID: node},
		Attrs: map[string]string{},
	}
	eng := &Engine{
		Graph:   graph,
		Options: RunOptions{RunID: "test-run"},
		Context: runtime.NewContext(),
	}
	exec := &Execution{
		Engine:      eng,
		Graph:       graph,
		Context:     eng.Context,
		WorktreeDir: t.TempDir(),
		LogsRoot:    t.TempDir(),
	}

	h := &ManagerLoopHandler{}
	out, err := h.Execute(context.Background(), exec, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should exhaust max_cycles, not fast-fail.
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL (max cycles), got %s", out.Status)
	}
	if !strings.Contains(out.FailureReason, "max cycles") {
		t.Errorf("expected max cycles reason, got: %s", out.FailureReason)
	}
}

func TestManagerLoop_MissingContext_ReturnsFail(t *testing.T) {
	h := &ManagerLoopHandler{}
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	out, err := h.Execute(context.Background(), nil, node)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
}

func TestFindExitNodeID_Found(t *testing.T) {
	// isTerminal checks for Msquare shape or id "exit"/"end".
	g := &model.Graph{
		Nodes: map[string]*model.Node{
			"start": {ID: "start", Attrs: map[string]string{"shape": "Mdiamond"}},
			"work":  {ID: "work", Attrs: map[string]string{"shape": "box"}},
			"exit":  {ID: "exit", Attrs: map[string]string{"shape": "Msquare"}},
		},
	}
	exitID := findExitNodeID(g)
	if exitID != "exit" {
		t.Errorf("expected 'exit', got %q", exitID)
	}
}

func TestFindExitNodeID_NilGraph(t *testing.T) {
	exitID := findExitNodeID(nil)
	if exitID != "" {
		t.Errorf("expected empty for nil graph, got %q", exitID)
	}
}
