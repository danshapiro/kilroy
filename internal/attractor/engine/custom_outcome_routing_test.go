package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// TestRun_BoxNodeCustomOutcome_RoutesWithoutRetry verifies that a shape=box
// (codergen) node returning a custom outcome (e.g. "needs_dod") routes via
// matching conditional edges instead of being retried as a failure.
//
// This is the canonical pattern from the reference dotfiles (consensus_task.dot,
// semport.dot) where box nodes return custom routing values like "needs_dod",
// "has_dod", "process", "done", "port", "skip".
//
// Per attractor-spec Section 3.3, edge selection evaluates conditions against
// the current outcome regardless of node shape. Custom outcomes that match
// outgoing edge conditions are routing decisions, not failures.
func TestRun_BoxNodeCustomOutcome_RoutesWithoutRetry(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Shim CLI that writes a custom outcome "needs_dod" to status.json.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
cat > status.json <<'JSON'
{"outcome":"needs_dod","notes":"definition_of_done.md does not exist"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	// Graph: check_dod is a box node that returns outcome=needs_dod.
	// It has conditional edges: needs_dod -> dod_gen, has_dod -> plan.
	// The engine should route to dod_gen without retrying check_dod.
	dot := []byte(`
digraph G {
  graph [goal="test custom outcome routing", default_max_retry=3]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  check_dod [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="check dod"]
  dod_gen [shape=parallelogram, tool_command="echo generated dod"]
  plan [shape=parallelogram, tool_command="echo planning"]

  start -> check_dod
  check_dod -> dod_gen [condition="outcome=needs_dod"]
  check_dod -> plan [condition="outcome=has_dod"]
  dod_gen -> exit
  plan -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "custom-outcome-route", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Run should succeed (routed through dod_gen -> exit).
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// check_dod/status.json should have the custom outcome, not "fail" or "max retries exceeded".
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "check_dod", "status.json"))
	if err != nil {
		t.Fatalf("read check_dod/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode check_dod/status.json: %v", err)
	}
	if out.Status != runtime.StageStatus("needs_dod") {
		t.Fatalf("check_dod status: got %q want %q", out.Status, "needs_dod")
	}

	// No retries should have been consumed on check_dod.
	cp, err := runtime.LoadCheckpoint(filepath.Join(res.LogsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	if v, ok := cp.NodeRetries["check_dod"]; ok && v > 0 {
		t.Fatalf("expected no retries for check_dod, got %d", v)
	}

	// dod_gen should have been executed (not plan), confirming correct routing.
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "dod_gen", "status.json")); err != nil {
		t.Fatalf("dod_gen should have been executed: %v", err)
	}
	// plan should NOT have been executed.
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "plan", "status.json")); err == nil {
		t.Fatalf("plan should NOT have been executed (wrong route taken)")
	}
}

// TestRun_BoxNodeCustomOutcome_NoMatchingEdge_Errors verifies that when a box
// node returns a custom outcome that does NOT match any outgoing edge condition,
// and no unconditional fallback edge exists, the engine treats this as a routing
// gap and errors. The user should add an unconditional edge for unmatched outcomes.
func TestRun_BoxNodeCustomOutcome_NoMatchingEdge_Errors(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Shim CLI that writes a custom outcome "unknown_value" -- no edge matches this.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
cat > status.json <<'JSON'
{"outcome":"unknown_value","notes":"unexpected state"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	// The box node returns "unknown_value" but edges only match "needs_dod" and "has_dod".
	// No unconditional edge exists — this is a routing gap.
	dot := []byte(`
digraph G {
  graph [goal="test unmatched custom outcome", default_max_retry=0]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  check_dod [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="check dod"]
  dod_gen [shape=parallelogram, tool_command="echo generated dod"]
  plan [shape=parallelogram, tool_command="echo planning"]

  start -> check_dod
  check_dod -> dod_gen [condition="outcome=needs_dod"]
  check_dod -> plan [condition="outcome=has_dod"]
  dod_gen -> exit
  plan -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "custom-outcome-nomatch", LogsRoot: logsRoot, AllowTestShim: true})

	// Routing gap: no condition matched "unknown_value", no unconditional edge.
	// Engine must return a non-nil error (not just a non-success FinalStatus).
	if err == nil {
		status := ""
		if res != nil {
			status = string(res.FinalStatus)
		}
		t.Fatalf("expected error from routing gap (no matching edge for unknown_value outcome), got nil error with status %q", status)
	}
}

// TestRun_BoxNodeCustomOutcome_ImplicitFanOut verifies that a box node
// returning a custom outcome fans out to multiple matching conditional edges.
// This is the consensus_task.dot pattern: check_dod -> dod_a, dod_b, dod_c.
func TestRun_BoxNodeCustomOutcome_ImplicitFanOut(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Shim CLI that returns outcome=needs_dod.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
cat > status.json <<'JSON'
{"outcome":"needs_dod","notes":"no dod found"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	// Multiple conditional edges match outcome=needs_dod -> implicit fan-out to dod_a and dod_b.
	dot := []byte(`
digraph G {
  graph [goal="test custom outcome fan-out", default_max_retry=3]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  check_dod [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="check dod"]
  dod_a [shape=parallelogram, tool_command="echo dod_a done"]
  dod_b [shape=parallelogram, tool_command="echo dod_b done"]
  merge [shape=parallelogram, tool_command="echo merged"]

  start -> check_dod
  check_dod -> dod_a [condition="outcome=needs_dod"]
  check_dod -> dod_b [condition="outcome=needs_dod"]
  dod_a -> merge
  dod_b -> merge
  merge -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "custom-outcome-fanout", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// check_dod should have the custom outcome, not a failure.
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "check_dod", "status.json"))
	if err != nil {
		t.Fatalf("read check_dod/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode check_dod/status.json: %v", err)
	}
	if out.Status != runtime.StageStatus("needs_dod") {
		t.Fatalf("check_dod status: got %q want %q", out.Status, "needs_dod")
	}

	// merge node should have been reached (proves fan-out completed and converged).
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "merge", "status.json")); err != nil {
		t.Fatalf("merge should have been executed (fan-out completed): %v", err)
	}
}

// TestRun_BoxNodeCustomOutcome_ContextDependentCondition verifies that
// hasMatchingOutgoingCondition evaluates against the live run context, not
// an empty context. Edges with context.* conditions must match correctly.
func TestRun_BoxNodeCustomOutcome_ContextDependentCondition(t *testing.T) {
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Shim CLI that writes a custom outcome "route_me" and sets a context var.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
cat > status.json <<'JSON'
{"outcome":"route_me","context_updates":{"phase":"dod"},"notes":"routing with context"}
JSON
echo '{"type":"done","text":"ok"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	// Edge condition uses both outcome AND context.phase — must use live context.
	dot := []byte(`
digraph G {
  graph [goal="test context-dependent custom outcome", default_max_retry=3]
  start [shape=Mdiamond]
  exit  [shape=Msquare]

  router [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="route based on context"]
  target_a [shape=parallelogram, tool_command="echo target_a reached"]
  target_b [shape=parallelogram, tool_command="echo target_b reached"]

  start -> router
  router -> target_a [condition="outcome=route_me && context.phase=dod"]
  router -> target_b [condition="outcome=route_me && context.phase=plan"]
  target_a -> exit
  target_b -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "custom-outcome-ctx", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// router should have the custom outcome.
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "router", "status.json"))
	if err != nil {
		t.Fatalf("read router/status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode router/status.json: %v", err)
	}
	if out.Status != runtime.StageStatus("route_me") {
		t.Fatalf("router status: got %q want %q", out.Status, "route_me")
	}

	// target_a should have been executed (phase=dod matched).
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "target_a", "status.json")); err != nil {
		t.Fatalf("target_a should have been executed: %v", err)
	}
	// target_b should NOT have been executed (phase=plan didn't match).
	if _, err := os.Stat(filepath.Join(res.LogsRoot, "target_b", "status.json")); err == nil {
		t.Fatalf("target_b should NOT have been executed (wrong context match)")
	}
}
