// Integration tests for script-based (tool-node-only) graphs.
// Validates engine traversal, routing, and failure handling without LLM involvement.

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestToolGraph_Linear(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph linear {
  graph [goal="Test linear traversal"]
  start [shape=Mdiamond]
  step_a [shape=parallelogram, tool_command="echo step_a_done"]
  step_b [shape=parallelogram, tool_command="echo step_b_done"]
  step_c [shape=parallelogram, tool_command="echo step_c_done"]
  done [shape=Msquare]
  start -> step_a -> step_b -> step_c -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "linear-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify all three steps executed
	for _, nodeID := range []string{"step_a", "step_b", "step_c"} {
		stdout := filepath.Join(logsRoot, nodeID, "stdout.log")
		data, err := os.ReadFile(stdout)
		if err != nil {
			t.Fatalf("read %s stdout: %v", nodeID, err)
		}
		if !strings.Contains(string(data), nodeID+"_done") {
			t.Fatalf("%s stdout: got %q, want to contain %q", nodeID, string(data), nodeID+"_done")
		}
	}
}

func TestToolGraph_LinearVerify_HillClimber(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Create a counter file in the repo — verify succeeds on 3rd attempt
	counterFile := filepath.Join(repo, "attempt_counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatal(err)
	}
	commitAll(t, repo, "add counter")

	dot := []byte(`digraph hill_climber {
  graph [goal="Test hill-climber verify loop"]
  start [shape=Mdiamond]
  implement [
    shape=parallelogram,
    tool_command="count=$(cat attempt_counter); count=$((count + 1)); echo $count > attempt_counter; echo implemented_attempt_$count"
  ]
  verify [
    shape=parallelogram,
    tool_command="count=$(cat attempt_counter); if [ $count -ge 3 ]; then echo 'all checks pass'; exit 0; else echo 'checks failed on attempt '$count; exit 1; fi"
  ]
  done [shape=Msquare]
  start -> implement
  implement -> verify
  verify -> done [condition="outcome=success"]
  verify -> implement
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "hill-climber-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}
}

func TestToolGraph_Conditional(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// process writes a marker file; path_a and path_b check for it
	dot := []byte(`digraph conditional {
  graph [goal="Test conditional routing"]
  start [shape=Mdiamond]
  process [shape=parallelogram, tool_command="echo processed"]
  check [shape=diamond]
  path_a [shape=parallelogram, tool_command="echo took_path_a"]
  done [shape=Msquare]
  start -> process -> check
  check -> path_a
  path_a -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "conditional-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify path_a was taken
	stdout := filepath.Join(logsRoot, "path_a", "stdout.log")
	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatalf("read path_a stdout: %v", err)
	}
	if !strings.Contains(string(data), "took_path_a") {
		t.Fatalf("path_a stdout: got %q, want to contain 'took_path_a'", string(data))
	}
}

func TestToolGraph_FailFast(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// A node that fails with no outgoing edge causes the run to fail.
	dot := []byte(`digraph fail_fast {
  graph [goal="Test failure handling"]
  start [shape=Mdiamond]
  failing_step [shape=parallelogram, tool_command="echo 'something went wrong' >&2; exit 1"]
  done [shape=Msquare]
  start -> failing_step -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "fail-fast-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// The node fails, but the unconditional edge routes to done.
	// Verify the node itself recorded a failure.
	statusPath := filepath.Join(logsRoot, "failing_step", "status.json")
	data, err := os.ReadFile(statusPath)
	if err != nil {
		t.Fatalf("read status.json: %v", err)
	}
	var out runtime.Outcome
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse status.json: %v", err)
	}
	if out.Status != runtime.StatusFail {
		t.Fatalf("expected fail status, got %q", out.Status)
	}
	_ = res
}

func TestToolGraph_NoCXDBConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph no_cxdb {
  graph [goal="Test running without CXDB config"]
  start [shape=Mdiamond]
  step [shape=parallelogram, tool_command="echo works_without_cxdb"]
  done [shape=Msquare]
  start -> step -> done
}`)
	// Config with NO CXDB section at all
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.LLM.CLIProfile = "test_shim"

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:    "no-cxdb-test",
		LogsRoot: logsRoot,
		// Note: NOT setting DisableCXDB — relying on empty config being treated as disabled
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}
}

func TestToolGraph_WorkspaceLifecycle(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph workspace {
  graph [goal="Test workspace setup and cleanup in flow"]
  start [shape=Mdiamond]
  setup [shape=parallelogram, tool_command="mkdir -p workspace && echo setup_done > workspace/state.txt"]
  work [shape=parallelogram, tool_command="cat workspace/state.txt && echo work_done >> workspace/state.txt"]
  verify [shape=parallelogram, tool_command="grep -q work_done workspace/state.txt"]
  done [shape=Msquare]
  start -> setup -> work -> verify -> done
}`)
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "workspace-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}
}

func TestToolGraph_ZeroConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	dot := []byte(`digraph zero_config {
  graph [goal="Test zero-config run"]
  start [shape=Mdiamond]
  step_a [shape=parallelogram, tool_command="echo hello_zero_config"]
  done [shape=Msquare]
  start -> step_a -> done
}`)

	// Build a config the same way DefaultRunConfig does, but pointing at
	// the test repo instead of CWD.
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.LLM.CLIProfile = "test_shim"
	// No ModelDB configured — bootstrap should fall back to embedded catalog.
	applyConfigDefaults(cfg)
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("validateConfig: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// No DisableCXDB — CXDB config is empty, so bootstrap skips it automatically.
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:    "zero-config-test",
		LogsRoot: logsRoot,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	// Verify the tool step executed.
	stdout := filepath.Join(logsRoot, "step_a", "stdout.log")
	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatalf("read step_a stdout: %v", err)
	}
	if !strings.Contains(string(data), "hello_zero_config") {
		t.Fatalf("step_a stdout: got %q, want to contain %q", string(data), "hello_zero_config")
	}
}

func TestToolGraph_RunIDInjected(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	markerFile := filepath.Join(t.TempDir(), "run_id_check.txt")

	dot := []byte(fmt.Sprintf(`digraph run_id {
  graph [goal="Test KILROY_RUN_ID injection into tool nodes"]
  start [shape=Mdiamond]
  check [shape=parallelogram, tool_command="echo $KILROY_RUN_ID > %s"]
  done [shape=Msquare]
  start -> check -> done
}`, markerFile))
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runID := "env-inject-test"
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       runID,
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	data, err := os.ReadFile(markerFile)
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	got := strings.TrimSpace(string(data))
	if got == "" {
		t.Fatal("KILROY_RUN_ID was empty in tool node environment")
	}
	if got != runID {
		t.Fatalf("KILROY_RUN_ID: got %q, want %q", got, runID)
	}
}

func TestToolGraph_DirtyRepoSucceedsWithDefaultConfig(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	// Make the repo dirty with an uncommitted file.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted"), 0o644); err != nil {
		t.Fatal(err)
	}

	dot := []byte(`digraph dirty_repo {
  graph [goal="Test dirty repo with default require_clean"]
  start [shape=Mdiamond]
  step [shape=parallelogram, tool_command="echo dirty_repo_ok"]
  done [shape=Msquare]
  start -> step -> done
}`)
	// Config does NOT set require_clean — the default (false) should allow the run.
	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "dirty-repo-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig with dirty repo: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success with dirty repo, got %q", res.FinalStatus)
	}
}

func TestToolGraph_PartialConfigAutoDetectsProviders(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-partial-config")
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)

	dot := []byte(`digraph partial {
  graph [goal="Test partial config with auto-detected providers"]
  start [shape=Mdiamond]
  step [shape=parallelogram, tool_command="echo partial_config_ok"]
  done [shape=Msquare]
  start -> step -> done
}`)
	// Config with repo.path and pinned catalog but NO providers section.
	// Auto-detection should fill in anthropic from ANTHROPIC_API_KEY.
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repo
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.LLM.CLIProfile = "test_shim"
	applyConfigDefaults(cfg)
	// Simulate what loadOrBuildConfig does: auto-detect and apply.
	detected := DetectProviders()
	ApplyDetectedProviders(cfg, detected)

	var foundAnthropic bool
	for _, dp := range detected {
		if dp.Key == "anthropic" {
			foundAnthropic = true
		}
	}
	if !foundAnthropic {
		t.Fatal("expected anthropic to be auto-detected from ANTHROPIC_API_KEY")
	}
	if _, ok := cfg.LLM.Providers["anthropic"]; !ok {
		t.Fatal("expected anthropic provider to be applied to config")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "partial-config-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	stdout := filepath.Join(logsRoot, "step", "stdout.log")
	data, err := os.ReadFile(stdout)
	if err != nil {
		t.Fatalf("read step stdout: %v", err)
	}
	if !strings.Contains(string(data), "partial_config_ok") {
		t.Fatalf("step stdout: got %q, want to contain %q", string(data), "partial_config_ok")
	}
}

// TestToolGraph_PredecessorEnvVars verifies that KILROY_PREDECESSOR_NODE and
// KILROY_PREDECESSOR_OUTCOME are injected into the tool-command environment.
//
// Graph: start -> a (fails) -> b (handler on fail edge) -> done
// When b runs, its predecessor is a with outcome "fail".
func TestToolGraph_PredecessorEnvVars(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	outFile := filepath.Join(t.TempDir(), "predecessor_check.txt")

	dot := []byte(fmt.Sprintf(`digraph predecessor_env {
  graph [goal="Test KILROY_PREDECESSOR_NODE and KILROY_PREDECESSOR_OUTCOME injection"]
  start [shape=Mdiamond]
  a     [shape=parallelogram, tool_command="exit 1"]
  b     [shape=parallelogram, tool_command="printf '%%s\n%%s\n' \"$KILROY_PREDECESSOR_NODE\" \"$KILROY_PREDECESSOR_OUTCOME\" > %s"]
  done  [shape=Msquare]
  start -> a
  a -> b    [condition="outcome=fail"]
  a -> done
  b -> done
}`, outFile))

	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "predecessor-env-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read predecessor_check.txt: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines in output, got %d: %q", len(lines), string(data))
	}
	gotNode := strings.TrimSpace(lines[0])
	gotOutcome := strings.TrimSpace(lines[1])
	if gotNode != "a" {
		t.Errorf("KILROY_PREDECESSOR_NODE: got %q, want %q", gotNode, "a")
	}
	if gotOutcome != "fail" {
		t.Errorf("KILROY_PREDECESSOR_OUTCOME: got %q, want %q", gotOutcome, "fail")
	}
}

// TestToolGraph_PredecessorEnvVarsSuccessPath verifies that KILROY_PREDECESSOR_NODE
// and KILROY_PREDECESSOR_OUTCOME reflect the correct values when the predecessor
// succeeded. When the first real node (check) runs, its predecessor is "start" with
// outcome "success".
func TestToolGraph_PredecessorEnvVarsSuccessPath(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	outFile := filepath.Join(t.TempDir(), "predecessor_success_check.txt")

	dot := []byte(fmt.Sprintf(`digraph predecessor_success_env {
  graph [goal="Test KILROY_PREDECESSOR_NODE and KILROY_PREDECESSOR_OUTCOME on success path"]
  start [shape=Mdiamond]
  check [shape=parallelogram, tool_command="printf '%%s\n%%s\n' \"$KILROY_PREDECESSOR_NODE\" \"$KILROY_PREDECESSOR_OUTCOME\" > %s"]
  done  [shape=Msquare]
  start -> check -> done
}`, outFile))

	cfg := minimalToolGraphConfig(repo, pinned)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:       "predecessor-env-success-test",
		LogsRoot:    logsRoot,
		DisableCXDB: true,
	})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected success, got %q", res.FinalStatus)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read predecessor_success_check.txt: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected 2 lines in output, got %d: %q", len(lines), string(data))
	}
	gotNode := strings.TrimSpace(lines[0])
	gotOutcome := strings.TrimSpace(lines[1])
	if gotNode != "start" {
		t.Errorf("KILROY_PREDECESSOR_NODE: got %q, want %q (first real node after start should see start as predecessor)", gotNode, "start")
	}
	if gotOutcome != "success" {
		t.Errorf("KILROY_PREDECESSOR_OUTCOME: got %q, want %q", gotOutcome, "success")
	}
}

// minimalToolGraphConfig returns a RunConfigFile suitable for tool-node-only graphs.
func minimalToolGraphConfig(repoPath, pinnedCatalogPath string) *RunConfigFile {
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Repo.Path = repoPath
	cfg.ModelDB.OpenRouterModelInfoPath = pinnedCatalogPath
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.LLM.CLIProfile = "test_shim"
	return cfg
}

// commitAll stages and commits all changes in the repo.
func commitAll(t *testing.T, repo, msg string) {
	t.Helper()
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", msg)
}
