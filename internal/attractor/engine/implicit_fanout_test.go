package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/dot"
	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
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

// ---------- Integration tests for implicit fan-out dispatch ----------

func initImplicitFanOutTestRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")
	return repo
}

// TestRun_ImplicitFanOut_EdgeTopology verifies that a 3-way unconditional fan-out
// from a regular (non-parallel) node is dispatched in parallel when all edges
// converge at a common downstream node.
func TestRun_ImplicitFanOut_EdgeTopology(t *testing.T) {
	repo := initImplicitFanOutTestRepo(t)

	dotSrc := []byte(`
digraph G {
  graph [goal="test implicit fan-out"]
  start  [shape=Mdiamond]
  source [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="source"]
  branch_a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  branch_b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  branch_c [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="c"]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="synth"]
  exit  [shape=Msquare]

  start -> source
  source -> branch_a
  source -> branch_b
  source -> branch_c
  branch_a -> synth
  branch_b -> synth
  branch_c -> synth
  synth -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// The engine should have written parallel_results.json for the source node
	// (the node that triggered implicit fan-out).
	resultsPath := filepath.Join(res.LogsRoot, "source", "parallel_results.json")
	assertExists(t, resultsPath)

	// Read and verify all 3 branches are present in the results.
	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(b, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 branch results, got %d", len(results))
	}

	// Collect branch keys and verify all 3 branches ran.
	keys := map[string]bool{}
	for _, r := range results {
		key, _ := r["branch_key"].(string)
		keys[key] = true
	}
	for _, want := range []string{"branch_a", "branch_b", "branch_c"} {
		if !keys[want] {
			t.Fatalf("missing branch key %q in results; got keys: %v", want, keys)
		}
	}

	// synth node should have run (it's the join target).
	assertExists(t, filepath.Join(res.LogsRoot, "synth", "status.json"))
}

// TestRun_ImplicitFanOut_WithTripleoctagonJoin verifies that implicit fan-out
// correctly detects and uses an explicit tripleoctagon join node.
func TestRun_ImplicitFanOut_WithTripleoctagonJoin(t *testing.T) {
	repo := initImplicitFanOutTestRepo(t)

	dotSrc := []byte(`
digraph G {
  graph [goal="test implicit fan-out with tripleoctagon"]
  start  [shape=Mdiamond]
  source [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="source"]
  branch_a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  branch_b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  join [shape=tripleoctagon]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="synth"]
  exit  [shape=Msquare]

  start -> source
  source -> branch_a
  source -> branch_b
  branch_a -> join
  branch_b -> join
  join -> synth
  synth -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// parallel_results.json should exist for the source node.
	assertExists(t, filepath.Join(res.LogsRoot, "source", "parallel_results.json"))

	// The tripleoctagon join node should have been executed (FanInHandler).
	joinStatusPath := filepath.Join(res.LogsRoot, "join", "status.json")
	assertExists(t, joinStatusPath)

	// synth should also have run after the join.
	assertExists(t, filepath.Join(res.LogsRoot, "synth", "status.json"))
}

// TestRun_ImplicitFanOut_ConditionalEdges verifies that when multiple conditional
// edges match the current outcome, they are dispatched as an implicit fan-out.
// Edges that do not match the condition should NOT be dispatched.
func TestRun_ImplicitFanOut_ConditionalEdges(t *testing.T) {
	repo := initImplicitFanOutTestRepo(t)

	// check is a diamond (conditional) node. After the preceding start node succeeds,
	// the outcome context is "success". The conditional handler passes through the
	// previous outcome. Edges with condition="outcome=success" match; the fallback
	// edge with condition="outcome=fail" does not.
	dotSrc := []byte(`
digraph G {
  graph [goal="test conditional implicit fan-out"]
  start  [shape=Mdiamond]
  check  [shape=diamond]
  branch_a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  branch_b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  fallback [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="fallback"]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="synth"]
  exit  [shape=Msquare]

  start -> check
  check -> branch_a [condition="outcome=success"]
  check -> branch_b [condition="outcome=success"]
  check -> fallback  [condition="outcome=fail"]
  branch_a -> synth
  branch_b -> synth
  fallback -> synth
  synth -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// parallel_results.json should exist for the check node.
	resultsPath := filepath.Join(res.LogsRoot, "check", "parallel_results.json")
	assertExists(t, resultsPath)

	// Read and verify that only the two success-condition branches ran.
	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(b, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 branch results (branch_a, branch_b), got %d", len(results))
	}

	keys := map[string]bool{}
	for _, r := range results {
		key, _ := r["branch_key"].(string)
		keys[key] = true
	}
	for _, want := range []string{"branch_a", "branch_b"} {
		if !keys[want] {
			t.Fatalf("missing branch key %q in results; got keys: %v", want, keys)
		}
	}
	if keys["fallback"] {
		t.Fatal("fallback branch should NOT have been dispatched (condition=outcome=fail)")
	}
}

func TestRun_SingleEdge_NoImplicitFanOut(t *testing.T) {
	repo := initImplicitFanOutTestRepo(t)

	dotSrc := []byte(`
digraph G {
  graph [goal="test no fan-out"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  start -> a
  a -> b
  b -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	// No parallel_results.json should exist — single edge, no fan-out.
	resultsPath := filepath.Join(res.LogsRoot, "a", "parallel_results.json")
	if _, err := os.Stat(resultsPath); err == nil {
		t.Fatalf("parallel_results.json should NOT exist for single-edge traversal")
	}
}
