package engine

// parity_matrix_test.go implements the spec §11.12 cross-feature parity matrix
// (22 rows) and the §11.13 integration smoke test. Each subtest exercises a
// specific capability end-to-end through the engine. See plan-parity-matrix.md
// for the coverage analysis showing which rows are new vs. wrappers of
// existing tests.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/dot"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/attractor/validate"
)

// parityInitRepo creates a temporary git repository suitable for engine.Run().
func parityInitRepo(t *testing.T) string {
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

// ---------------------------------------------------------------------------
// §11.12 Cross-Feature Parity Matrix -- 22 rows
// ---------------------------------------------------------------------------

// Row 1: Parse a simple linear pipeline (start -> A -> B -> done)
func TestParityMatrix_Row01_ParseLinearPipeline(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  done [shape=Msquare]
  start -> a -> b -> done
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(g.Nodes) != 4 {
		t.Fatalf("nodes: got %d want 4", len(g.Nodes))
	}
	if len(g.Edges) != 3 {
		t.Fatalf("edges: got %d want 3", len(g.Edges))
	}
	for _, id := range []string{"start", "a", "b", "done"} {
		if _, ok := g.Nodes[id]; !ok {
			t.Fatalf("missing node %q", id)
		}
	}
}

// Row 2: Parse a pipeline with graph-level attributes (goal, label)
func TestParityMatrix_Row02_ParseGraphAttributes(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  graph [goal="Build a CLI tool", label="My Pipeline"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := g.Attrs["goal"]; got != "Build a CLI tool" {
		t.Fatalf("goal: got %q want %q", got, "Build a CLI tool")
	}
	if got := g.Attrs["label"]; got != "My Pipeline" {
		t.Fatalf("label: got %q want %q", got, "My Pipeline")
	}
}

// Row 3: Parse multi-line node attributes
func TestParityMatrix_Row03_ParseMultiLineNodeAttributes(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  worker [
    shape=box,
    llm_provider=openai,
    llm_model=gpt-5.2,
    prompt="Build the feature",
    max_retries=3
  ]
  start -> worker -> exit
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	n := g.Nodes["worker"]
	if n == nil {
		t.Fatalf("worker node not found")
	}
	if got := n.Attrs["shape"]; got != "box" {
		t.Fatalf("shape: got %q want %q", got, "box")
	}
	if got := n.Attrs["llm_provider"]; got != "openai" {
		t.Fatalf("llm_provider: got %q want %q", got, "openai")
	}
	if got := n.Attrs["llm_model"]; got != "gpt-5.2" {
		t.Fatalf("llm_model: got %q want %q", got, "gpt-5.2")
	}
	if got := n.Attrs["prompt"]; got != "Build the feature" {
		t.Fatalf("prompt: got %q want %q", got, "Build the feature")
	}
	if got := n.Attrs["max_retries"]; got != "3" {
		t.Fatalf("max_retries: got %q want %q", got, "3")
	}
}

// Row 4: Validate: missing start node -> error
func TestParityMatrix_Row04_ValidateMissingStartNode(t *testing.T) {
	_, _, err := Prepare([]byte(`
digraph G {
  exit [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  a -> exit
}
`))
	if err == nil {
		t.Fatalf("expected validation error for missing start node, got nil")
	}
	if !strings.Contains(err.Error(), "start") {
		t.Fatalf("error should mention start: %v", err)
	}
}

// Row 5: Validate: missing exit node -> error
func TestParityMatrix_Row05_ValidateMissingExitNode(t *testing.T) {
	_, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
}
`))
	if err == nil {
		t.Fatalf("expected validation error for missing exit node, got nil")
	}
	if !strings.Contains(err.Error(), "terminal") && !strings.Contains(err.Error(), "exit") {
		t.Fatalf("error should mention terminal/exit: %v", err)
	}
}

// Row 6: Validate: orphan node -> diagnostic
// NOTE: The spec says "warning" but the current implementation treats
// unreachable nodes as ERROR severity (lintReachability). This test verifies
// the diagnostic is emitted; the severity discrepancy is tracked separately.
func TestParityMatrix_Row06_ValidateOrphanNodeDiagnostic(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  a      [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  orphan [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	diags := validate.Validate(g)
	found := false
	for _, d := range diags {
		if d.Rule == "reachability" && d.NodeID == "orphan" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected reachability diagnostic for orphan node; got: %+v", diags)
	}
}

// Row 7: Execute a linear 3-node pipeline end-to-end
func TestParityMatrix_Row07_ExecuteLinear3NodePipeline(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do task a"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	// Verify stage artifacts exist.
	assertExists(t, filepath.Join(res.LogsRoot, "a", "status.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "prompt.md"))
	assertExists(t, filepath.Join(res.LogsRoot, "a", "response.md"))
}

// Row 8: Execute with conditional branching (success/fail paths)
func TestParityMatrix_Row08_ExecuteConditionalBranching(t *testing.T) {
	repo := parityInitRepo(t)
	// tool_command succeeds, so condition="outcome=success" edge should win.
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  worker [shape=parallelogram, tool_command="echo ok"]
  good   [shape=parallelogram, tool_command="echo good"]
  bad    [shape=parallelogram, tool_command="echo bad"]
  start -> worker
  worker -> good [condition="outcome=success"]
  worker -> bad  [condition="outcome=fail"]
  good -> exit
  bad -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	// "good" should have executed; "bad" should not.
	assertExists(t, filepath.Join(res.LogsRoot, "good", "status.json"))
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "bad", "status.json")); err == nil {
		t.Fatalf("bad node should not have executed")
	}
}

// Row 9: Execute with retry on failure (max_retries=2)
func TestParityMatrix_Row09_ExecuteRetryOnFailure(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [
    shape=parallelogram,
    max_retries=2,
    tool_command="test -f .attempt && echo ok || (touch .attempt; echo fail; exit 1)"
  ]
  start -> t -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "t", "status.json"))
	if err != nil {
		t.Fatalf("read t status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode t status.json: %v", err)
	}
	if out.Status != runtime.StatusSuccess {
		t.Fatalf("t outcome: got %q want %q", out.Status, runtime.StatusSuccess)
	}
}

// Row 10: Goal gate blocks exit when unsatisfied
func TestParityMatrix_Row10_GoalGateBlocksExit(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  gate [
    shape=parallelogram,
    goal_gate=true,
    tool_command="echo fail; exit 1"
  ]
  start -> gate -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err == nil {
		t.Fatalf("expected error for unsatisfied goal gate, got nil")
	}
	if !strings.Contains(err.Error(), "goal gate") {
		t.Fatalf("error should mention goal gate: %v", err)
	}
}

// Row 11: Goal gate allows exit when all satisfied
func TestParityMatrix_Row11_GoalGateAllowsExit(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  gate [
    shape=parallelogram,
    goal_gate=true,
    retry_target=gate,
    max_retries=0,
    tool_command="test -f .attempt && echo ok || (touch .attempt; echo fail; exit 1)"
  ]
  start -> gate -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
}

// Row 12: Wait.human presents choices and routes on selection
func TestParityMatrix_Row12_WaitHumanRoutesOnSelection(t *testing.T) {
	repo := parityInitRepo(t)
	dotSrc := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  gate  [shape=hexagon, label="Gate"]
  approve [shape=parallelogram, tool_command="echo approve"]
  fix     [shape=parallelogram, tool_command="echo fix"]
  exit  [shape=Msquare]

  start -> gate
  gate -> approve [label="[A] Approve"]
  gate -> fix     [label="[F] Fix"]
  approve -> exit
  fix -> exit
}
`)
	g, _, err := Prepare(dotSrc)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "pm12", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}

	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       append([]byte{}, dotSrc...),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &QueueInterviewer{Answers: []Answer{{Value: "F"}}},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.RunBranch = fmt.Sprintf("%s/%s", opts.RunBranchPrefix, opts.RunID)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err = eng.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// "fix" should have executed; "approve" should not.
	assertExists(t, filepath.Join(logsRoot, "fix", "status.json"))
	if _, err := os.Stat(filepath.Join(logsRoot, "approve", "status.json")); err == nil {
		t.Fatalf("approve node should not have executed")
	}
}

// Row 13: Edge selection: condition match wins over weight
func TestParityMatrix_Row13_EdgeConditionBeatsWeight(t *testing.T) {
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

// Row 14: Edge selection: weight breaks ties for unconditional edges
func TestParityMatrix_Row14_EdgeWeightBreaksTies(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  high [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  low  [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> low  [weight=1]
  a -> high [weight=100]
  low -> exit
  high -> exit
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
	if e == nil || e.To != "high" {
		t.Fatalf("edge: got %+v want to=high (highest weight)", e)
	}
}

// Row 15: Edge selection: lexical tiebreak as final fallback
func TestParityMatrix_Row15_EdgeLexicalTiebreak(t *testing.T) {
	g, err := dot.Parse([]byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  beta  [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  alpha [shape=box, llm_provider=openai, llm_model=gpt-5.2]
  start -> a
  a -> beta  [weight=5]
  a -> alpha [weight=5]
  beta -> exit
  alpha -> exit
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
	// Same weight -> lexical by target node ID -> "alpha" < "beta"
	if e == nil || e.To != "alpha" {
		t.Fatalf("edge: got %+v want to=alpha (lexical tiebreak)", e)
	}
}

// Row 16: Context updates from one node are visible to the next
func TestParityMatrix_Row16_ContextUpdatesVisibleToNext(t *testing.T) {
	repo := parityInitRepo(t)

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=diamond, type="setctx"]
  exit [shape=Msquare]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "pm16", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       []byte(""),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.Registry.Register("setctx", &setContextHandler{})
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got := cp.ContextValues["k"]; got != "v" {
		t.Fatalf("checkpoint context k: got %v want v", got)
	}
}

// Row 17: Checkpoint save and resume produces same result
func TestParityMatrix_Row17_CheckpointSaveAndResume(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do a"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	// Verify checkpoint exists.
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	assertExists(t, cpPath)
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}

	// The checkpoint should record the exit node and completed nodes.
	if cp.CurrentNode == "" {
		t.Fatalf("checkpoint current_node is empty")
	}
	if len(cp.CompletedNodes) == 0 {
		t.Fatalf("checkpoint completed_nodes is empty")
	}

	// Rewinding checkpoint to start for a resume test.
	cp.CurrentNode = "start"
	cp.CompletedNodes = []string{"start"}
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}
	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if res2.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("resumed final status: got %q want %q", res2.FinalStatus, runtime.FinalSuccess)
	}
}

// Row 18: Stylesheet applies model override to nodes by shape
// The stylesheet provides default values for properties that are NOT already
// set on the node (spec §8: "only set properties that are missing"). So to
// test that the stylesheet applies, the node must NOT have the property pre-set.
func TestParityMatrix_Row18_StylesheetAppliesModelOverrideByShape(t *testing.T) {
	// Test 1: Verify at parse/prepare level that stylesheet fills in missing llm_model.
	g, _, err := Prepare([]byte(`
digraph G {
  graph [
    goal="test",
    model_stylesheet="box { llm_model: custom-model-42; llm_provider: openai; }"
  ]
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  worker [shape=box, prompt="do work"]
  cond   [shape=diamond]
  start -> worker -> cond -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Stylesheet should fill in box-shaped node's llm_model (was missing).
	if got := g.Nodes["worker"].Attrs["llm_model"]; got != "custom-model-42" {
		t.Fatalf("worker llm_model: got %q want %q", got, "custom-model-42")
	}
	if got := g.Nodes["worker"].Attrs["llm_provider"]; got != "openai" {
		t.Fatalf("worker llm_provider: got %q want %q", got, "openai")
	}
	// Diamond-shaped node should NOT be affected by box selector.
	if got := g.Nodes["cond"].Attrs["llm_model"]; got == "custom-model-42" {
		t.Fatalf("cond should not have llm_model=custom-model-42 (it's diamond, not box)")
	}

	// Test 2: End-to-end through Run() to verify stylesheet + execution.
	repo := parityInitRepo(t)
	dotSrc := []byte(`
digraph G {
  graph [
    goal="test",
    model_stylesheet="box { llm_model: e2e-test-model; llm_provider: openai; }"
  ]
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  worker [shape=box, prompt="do work"]
  start -> worker -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
}

// Row 19: Prompt variable expansion ($goal) works
func TestParityMatrix_Row19_PromptVariableExpansion(t *testing.T) {
	g, _, err := Prepare([]byte(`
digraph G {
  graph [goal="Build a REST API"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Implement: $goal"]
  start -> a -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got := g.Nodes["a"].Attr("prompt", ""); got != "Implement: Build a REST API" {
		t.Fatalf("prompt: got %q want %q", got, "Implement: Build a REST API")
	}
}

// Row 20: Parallel fan-out and fan-in complete correctly
func TestParityMatrix_Row20_ParallelFanOutAndFanIn(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph P {
  graph [goal="test"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="branch a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="branch b"]
  join [shape=tripleoctagon]
  exit [shape=Msquare]

  start -> par
  par -> a
  par -> b
  a -> join
  b -> join
  join -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	assertExists(t, filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "join", "status.json"))
}

// Row 21: Custom handler registration and execution works
// This test registers a custom handler and exercises it through the engine's
// run() method, verifying that custom handlers work end-to-end.

type parityCustomHandler struct{}

func (h *parityCustomHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	marker := filepath.Join(exec.WorktreeDir, "custom_handler_ran.txt")
	if err := os.WriteFile(marker, []byte("yes"), 0o644); err != nil {
		return runtime.Outcome{Status: runtime.StatusFail}, err
	}
	return runtime.Outcome{
		Status:         runtime.StatusSuccess,
		ContextUpdates: map[string]any{"custom_ran": true},
	}, nil
}

func TestParityMatrix_Row21_CustomHandlerRegistration(t *testing.T) {
	repo := parityInitRepo(t)

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  custom [shape=diamond, type="parity_custom"]
  exit [shape=Msquare]
  start -> custom -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "pm21", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       []byte(""),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.Registry.Register("parity_custom", &parityCustomHandler{})
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err = eng.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// Verify the custom handler ran by checking its side effect.
	b, err := os.ReadFile(filepath.Join(eng.WorktreeDir, "custom_handler_ran.txt"))
	if err != nil {
		t.Fatalf("custom handler marker file: %v", err)
	}
	if strings.TrimSpace(string(b)) != "yes" {
		t.Fatalf("custom handler marker: got %q want %q", string(b), "yes")
	}

	// Verify context update from custom handler.
	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if got := cp.ContextValues["custom_ran"]; got != true {
		t.Fatalf("checkpoint context custom_ran: got %v want true", got)
	}
}

// Row 22: Pipeline with 10+ nodes completes without errors
func TestParityMatrix_Row22_TenPlusNodesPipeline(t *testing.T) {
	repo := parityInitRepo(t)
	dot := []byte(`
digraph G {
  graph [goal="test large pipeline"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t01 [shape=parallelogram, tool_command="echo step01"]
  t02 [shape=parallelogram, tool_command="echo step02"]
  t03 [shape=parallelogram, tool_command="echo step03"]
  t04 [shape=parallelogram, tool_command="echo step04"]
  t05 [shape=parallelogram, tool_command="echo step05"]
  t06 [shape=parallelogram, tool_command="echo step06"]
  t07 [shape=parallelogram, tool_command="echo step07"]
  t08 [shape=parallelogram, tool_command="echo step08"]
  t09 [shape=parallelogram, tool_command="echo step09"]
  t10 [shape=parallelogram, tool_command="echo step10"]
  t11 [shape=parallelogram, tool_command="echo step11"]
  t12 [shape=parallelogram, tool_command="echo step12"]
  start -> t01 -> t02 -> t03 -> t04 -> t05 -> t06 -> t07 -> t08 -> t09 -> t10 -> t11 -> t12 -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	// Verify all 12 tool nodes produced status.json.
	for i := 1; i <= 12; i++ {
		name := fmt.Sprintf("t%02d", i)
		assertExists(t, filepath.Join(res.LogsRoot, name, "status.json"))
	}
}

// ---------------------------------------------------------------------------
// §11.13 Integration Smoke Test
// ---------------------------------------------------------------------------

// TestIntegrationSmokeTest_Section11_13 exercises the full pipeline lifecycle
// as defined in spec §11.13: parse -> validate -> execute -> verify artifacts
// -> verify goal gate -> verify checkpoint.
func TestIntegrationSmokeTest_Section11_13(t *testing.T) {
	repo := parityInitRepo(t)

	// The DOT graph from spec §11.13 (adapted for SimulatedCodergenBackend).
	dotSrc := []byte(`
digraph test_pipeline {
    graph [goal="Create a hello world Python script"]

    start       [shape=Mdiamond]
    plan        [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Plan how to create a hello world script for: $goal"]
    implement   [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Write the code based on the plan", goal_gate=true]
    review      [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="Review the code for correctness"]
    done        [shape=Msquare]

    start -> plan
    plan -> implement
    implement -> review [condition="outcome=success"]
    implement -> plan   [condition="outcome=fail", label="Retry"]
    review -> done      [condition="outcome=success"]
    review -> implement [condition="outcome=fail", label="Fix"]
}
`)

	// --- Step 1: Parse ---
	g, err := dot.Parse(dotSrc)
	if err != nil {
		t.Fatalf("Step 1 (Parse): %v", err)
	}
	if got := g.Attrs["goal"]; got != "Create a hello world Python script" {
		t.Fatalf("Step 1: goal = %q, want %q", got, "Create a hello world Python script")
	}
	if got := len(g.Nodes); got != 5 {
		t.Fatalf("Step 1: nodes = %d, want 5", got)
	}
	if got := len(g.Edges); got != 6 {
		t.Fatalf("Step 1: edges = %d, want 6", got)
	}

	// --- Step 2: Validate ---
	g2, diags, err := Prepare(dotSrc)
	if err != nil {
		t.Fatalf("Step 2 (Validate): %v", err)
	}
	_ = g2
	for _, d := range diags {
		if d.Severity == validate.SeverityError {
			t.Fatalf("Step 2: unexpected error diagnostic: %+v", d)
		}
	}

	// --- Step 3: Execute with simulated backend ---
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Step 3 (Execute): %v", err)
	}

	// --- Step 4: Verify outcome and artifacts ---
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("Step 4: status = %q, want %q", res.FinalStatus, runtime.FinalSuccess)
	}
	for _, node := range []string{"plan", "implement", "review"} {
		for _, file := range []string{"prompt.md", "response.md", "status.json"} {
			path := filepath.Join(res.LogsRoot, node, file)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("Step 4: artifact %s/%s missing: %v", node, file, err)
			}
		}
	}

	// --- Step 5: Verify goal gate (implement has goal_gate=true) ---
	implStatus, err := os.ReadFile(filepath.Join(res.LogsRoot, "implement", "status.json"))
	if err != nil {
		t.Fatalf("Step 5: read implement status.json: %v", err)
	}
	implOutcome, err := runtime.DecodeOutcomeJSON(implStatus)
	if err != nil {
		t.Fatalf("Step 5: decode implement status.json: %v", err)
	}
	if implOutcome.Status != runtime.StatusSuccess {
		t.Fatalf("Step 5: implement outcome = %q, want %q (goal gate must be satisfied)", implOutcome.Status, runtime.StatusSuccess)
	}

	// --- Step 6: Verify checkpoint ---
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("Step 6: LoadCheckpoint: %v", err)
	}
	// The final node should be "done" (exit).
	if cp.CurrentNode != "done" {
		t.Fatalf("Step 6: current_node = %q, want %q", cp.CurrentNode, "done")
	}
	// All three worker nodes must be in completed_nodes.
	completed := map[string]bool{}
	for _, n := range cp.CompletedNodes {
		completed[n] = true
	}
	for _, want := range []string{"plan", "implement", "review"} {
		if !completed[want] {
			t.Fatalf("Step 6: %q not in completed_nodes: %v", want, cp.CompletedNodes)
		}
	}
}
