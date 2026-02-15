package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestRun_LoopRestartCreatesNewLogDirectory(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Graph: start -> work -> check
	//   check -> exit [condition="outcome=success"]
	//   check -> work [condition="outcome=fail", loop_restart=true]
	//
	// The backend returns fail on the first call to "work", success on the second.
	dot := []byte(`
digraph G {
  graph [goal="test loop restart", default_max_retry=0]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var callCount atomic.Int32
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			n := callCount.Add(1)
			if node.ID == "work" && n == 1 {
				return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary network error: connection reset by peer"}, nil
			}
			return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-restart", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-restart"

	res, err := eng.run(ctx)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("FinalStatus = %v, want success", res.FinalStatus)
	}

	// Verify the backend was called twice for "work" (once per iteration).
	if got := callCount.Load(); got < 2 {
		t.Fatalf("backend call count = %d, want >= 2", got)
	}

	// Verify a restart directory was created.
	restartDir := filepath.Join(logsRoot, "restart-1")
	if _, err := os.Stat(restartDir); err != nil {
		t.Fatalf("expected restart-1 directory to exist: %v", err)
	}

	// Verify manifest.json exists in the restart directory (review fix: metadata in restart dirs).
	manifestPath := filepath.Join(restartDir, "manifest.json")
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("expected manifest.json in restart dir: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("invalid manifest.json: %v", err)
	}
	if manifest["run_id"] != "test-restart" {
		t.Errorf("manifest run_id = %v, want %q", manifest["run_id"], "test-restart")
	}

	// Verify context was reset on restart (review fix: no stale context bleed).
	// After a successful restart, context should have graph-level attrs but NOT
	// node outcomes from the first (failed) iteration.
	if _, found := eng.Context.Get("node.work.outcome"); found {
		t.Error("stale node outcome leaked across restart boundary")
	}
}

func TestLoopRestart_ResetsRetryBudgetPerIteration(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test retry budget reset", max_restarts="3"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, max_retries="1", prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	workCallsByLogsRoot := map[string]int{}
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			if node.ID != "work" {
				return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
			}
			logRoot := strings.TrimSpace(exec.LogsRoot)
			workCallsByLogsRoot[logRoot]++
			call := workCallsByLogsRoot[logRoot]
			if strings.HasSuffix(logRoot, "restart-1") {
				if call == 1 {
					return "retry", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary upstream timeout"}, nil
				}
				return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
			}
			return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary upstream timeout"}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-retry-reset", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-retry-reset"

	res, err := eng.run(ctx)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("FinalStatus = %v, want success", res.FinalStatus)
	}

	baseProgress := readProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
	foundLoopRestart := false
	for _, ev := range baseProgress {
		if strings.TrimSpace(fmt.Sprint(ev["event"])) != "loop_restart" {
			continue
		}
		foundLoopRestart = true
		if reset, ok := ev["retry_budget_reset"].(bool); !ok || !reset {
			t.Fatalf("expected loop_restart event to include retry_budget_reset=true: %#v", ev)
		}
	}
	if !foundLoopRestart {
		t.Fatalf("expected loop_restart event in base progress log")
	}

	restartProgress := readProgressEvents(t, filepath.Join(logsRoot, "restart-1", "progress.ndjson"))
	retryCount := -1
	for _, ev := range restartProgress {
		if strings.TrimSpace(fmt.Sprint(ev["event"])) != "stage_retry_sleep" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(ev["node_id"])) != "work" {
			continue
		}
		retryCount = progressIntValue(ev["retries"])
		break
	}
	if retryCount != 1 {
		t.Fatalf("expected restart iteration retry counter to reset to 1, got %d", retryCount)
	}
}

func TestRun_LoopRestartLimitExceeded(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Always fail, with max_restarts=2 so we hit the limit quickly.
	dot := []byte(`
digraph G {
  graph [goal="test limit", max_restarts="2", loop_restart_signature_limit="99"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary upstream failure: 503 service unavailable"}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-limit", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-limit"

	_, err = eng.run(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "loop_restart limit exceeded") {
		t.Fatalf("expected loop_restart limit error, got: %v", err)
	}
}

func TestRun_LoopRestartLimitExceeded_WritesTerminalFinalJSON(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test terminal final", max_restarts="1"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary network error: connection reset by peer"}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-final-on-limit", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-final-on-limit"

	_, err = eng.run(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "loop_restart limit exceeded") {
		t.Fatalf("expected loop_restart limit error, got: %v", err)
	}

	baseFinalPath := filepath.Join(logsRoot, "final.json")
	baseBytes, err := os.ReadFile(baseFinalPath)
	if err != nil {
		t.Fatalf("read base final.json: %v", err)
	}
	var baseFinal runtime.FinalOutcome
	if err := json.Unmarshal(baseBytes, &baseFinal); err != nil {
		t.Fatalf("unmarshal base final.json: %v", err)
	}
	if baseFinal.Status != runtime.FinalFail {
		t.Fatalf("base final status = %q, want %q", baseFinal.Status, runtime.FinalFail)
	}
	if !strings.Contains(baseFinal.FailureReason, "loop_restart limit exceeded") {
		t.Fatalf("base final failure_reason = %q, want loop_restart limit", baseFinal.FailureReason)
	}

	restartFinalPath := filepath.Join(logsRoot, "restart-1", "final.json")
	restartBytes, err := os.ReadFile(restartFinalPath)
	if err != nil {
		t.Fatalf("read restart final.json: %v", err)
	}
	var restartFinal runtime.FinalOutcome
	if err := json.Unmarshal(restartBytes, &restartFinal); err != nil {
		t.Fatalf("unmarshal restart final.json: %v", err)
	}
	if restartFinal.Status != runtime.FinalFail {
		t.Fatalf("restart final status = %q, want %q", restartFinal.Status, runtime.FinalFail)
	}
}

func TestRun_LoopRestartBlockedForDeterministicFailureClass(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test deterministic block", max_restarts="10"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "compile error: missing symbol TraceGlyph"}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-deterministic-block", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-deterministic-block"

	_, err = eng.run(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "loop_restart blocked") {
		t.Fatalf("expected loop_restart blocked error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(logsRoot, "restart-1")); !os.IsNotExist(statErr) {
		t.Fatalf("unexpected restart-1 directory created (err=%v)", statErr)
	}

	finalBytes, err := os.ReadFile(filepath.Join(logsRoot, "final.json"))
	if err != nil {
		t.Fatalf("read final.json: %v", err)
	}
	var final runtime.FinalOutcome
	if err := json.Unmarshal(finalBytes, &final); err != nil {
		t.Fatalf("unmarshal final.json: %v", err)
	}
	if final.Status != runtime.FinalFail {
		t.Fatalf("final status = %q, want %q", final.Status, runtime.FinalFail)
	}
	if !strings.Contains(final.FailureReason, "failure_class=deterministic") {
		t.Fatalf("final failure_reason = %q, want deterministic failure class", final.FailureReason)
	}
}

func TestRun_LoopRestartCircuitBreakerOnRepeatedSignature(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test circuit breaker", max_restarts="20", loop_restart_signature_limit="2"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary network error: connection reset by peer"}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-circuit-breaker", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-circuit-breaker"

	_, err = eng.run(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "loop_restart circuit breaker") {
		t.Fatalf("expected circuit breaker error, got: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(logsRoot, "restart-1")); statErr != nil {
		t.Fatalf("expected restart-1 directory to exist: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(logsRoot, "restart-2")); !os.IsNotExist(statErr) {
		t.Fatalf("expected restart-2 to not exist after circuit breaker (err=%v)", statErr)
	}

	finalBytes, err := os.ReadFile(filepath.Join(logsRoot, "final.json"))
	if err != nil {
		t.Fatalf("read final.json: %v", err)
	}
	var final runtime.FinalOutcome
	if err := json.Unmarshal(finalBytes, &final); err != nil {
		t.Fatalf("unmarshal final.json: %v", err)
	}
	if final.Status != runtime.FinalFail {
		t.Fatalf("final status = %q, want %q", final.Status, runtime.FinalFail)
	}
	if !strings.Contains(final.FailureReason, "loop_restart circuit breaker") {
		t.Fatalf("final failure_reason = %q, want circuit breaker", final.FailureReason)
	}
}

func TestClassifyFailureClass_StreamDisconnectIsTransient(t *testing.T) {
	cases := []struct {
		reason    string
		wantClass string
	}{
		{"codex stream disconnected before completion", failureClassTransientInfra},
		{"stream closed before response.completed", failureClassTransientInfra},
		{"exit status 1", failureClassDeterministic},
	}
	for _, tc := range cases {
		t.Run(tc.reason, func(t *testing.T) {
			out := runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: tc.reason,
			}
			got := classifyFailureClass(out)
			if got != tc.wantClass {
				t.Fatalf("classifyFailureClass(%q): got %q want %q", tc.reason, got, tc.wantClass)
			}
		})
	}
}

func TestRun_StuckCycleNodeVisitLimit(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Graph where implement always succeeds but verify always fails,
	// and the retry edge goes back to implement WITHOUT loop_restart.
	// Either the signature-based cycle breaker or the node-visit limit
	// will catch this — the key invariant is that the run terminates.
	dot := []byte(`
digraph G {
  graph [goal="test stuck cycle", max_node_visits="5"]
  start  [shape=Mdiamond]
  exit   [shape=Msquare]
  impl   [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="implement"]
  verify [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="verify"]
  check  [shape=diamond]
  start -> impl -> verify -> check
  check -> exit [condition="outcome=success"]
  check -> impl [condition="outcome=fail"]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var callCount atomic.Int64
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			callCount.Add(1)
			if node.ID == "impl" {
				return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
			}
			// verify always fails with varying messages (simulating AI variance)
			n := callCount.Load()
			return "fail", &runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: fmt.Sprintf("test failure variant %d: missing dependency xyz", n),
			}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-stuck-cycle", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-stuck-cycle"

	_, err = eng.run(ctx)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	// Accept either the signature-based cycle breaker or the node-visit limit.
	isCycleError := strings.Contains(err.Error(), "stuck in a cycle") ||
		strings.Contains(err.Error(), "deterministic failure cycle")
	if !isCycleError {
		t.Fatalf("expected stuck-cycle or deterministic-failure-cycle error, got: %v", err)
	}
	// With max_node_visits=5 and >=, impl halts on its 5th visit (before executing),
	// so 4 complete cycles: impl(4) + verify(4) = 8 backend calls max.
	// The signature breaker may fire even sooner (at 3 repeated signatures).
	if callCount.Load() > 12 {
		t.Fatalf("expected backend called <= 12 times, got %d", callCount.Load())
	}

	// Verify final.json was written with failure
	finalBytes, err := os.ReadFile(filepath.Join(logsRoot, "final.json"))
	if err != nil {
		t.Fatalf("read final.json: %v", err)
	}
	var final runtime.FinalOutcome
	if err := json.Unmarshal(finalBytes, &final); err != nil {
		t.Fatalf("unmarshal final.json: %v", err)
	}
	if final.Status != runtime.FinalFail {
		t.Fatalf("final status = %q, want %q", final.Status, runtime.FinalFail)
	}
	isFinalCycleError := strings.Contains(final.FailureReason, "stuck in a cycle") ||
		strings.Contains(final.FailureReason, "deterministic failure cycle")
	if !isFinalCycleError {
		t.Fatalf("final failure_reason = %q, want stuck-cycle or deterministic-failure-cycle", final.FailureReason)
	}
}

func TestMaxNodeVisits_GraphAttrOverride(t *testing.T) {
	g := &model.Graph{Attrs: map[string]string{"max_node_visits": "7"}}
	if got := maxNodeVisits(g); got != 7 {
		t.Fatalf("maxNodeVisits with attr=7: got %d want 7", got)
	}
}

func TestMaxNodeVisits_DefaultWhenMissing(t *testing.T) {
	g := &model.Graph{Attrs: map[string]string{}}
	if got := maxNodeVisits(g); got != defaultMaxNodeVisits {
		t.Fatalf("maxNodeVisits default: got %d want %d", got, defaultMaxNodeVisits)
	}
	if got := maxNodeVisits(nil); got != defaultMaxNodeVisits {
		t.Fatalf("maxNodeVisits nil: got %d want %d", got, defaultMaxNodeVisits)
	}
}

func readProgressEvents(t *testing.T, path string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode progress event %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func progressIntValue(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, _ := n.Int64()
		return int(i)
	case string:
		i, err := strconv.Atoi(strings.TrimSpace(n))
		if err != nil {
			return -1
		}
		return i
	default:
		return -1
	}
}

// countingBackend is a test backend with a configurable function.
type countingBackend struct {
	fn func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error)
}

func (b *countingBackend) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	return b.fn(ctx, exec, node, prompt)
}

func TestLoopRestart_PersistsContextKeys(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Graph with loop_restart_persist_keys that preserves "completed_features"
	// across restarts. The "work" node sets completed_features in context on
	// each iteration. After a restart, the value should carry over.
	dot := []byte(`
digraph G {
  graph [goal="test context persistence", default_max_retry=0, loop_restart_persist_keys="completed_features,skipped_features"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var callCount atomic.Int32
	var observedCompletedFeatures string
	var observedIterationCount any
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			if node.ID != "work" {
				return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
			}
			n := callCount.Add(1)
			if n == 1 {
				// First iteration: set completed_features and fail to trigger restart.
				return "fail", &runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: "temporary network error: connection reset",
					ContextUpdates: map[string]any{
						"completed_features": "feature-1",
						"ephemeral_state":    "should-not-persist",
					},
				}, nil
			}
			// Second iteration: check if completed_features persisted.
			observedCompletedFeatures = exec.Context.GetString("completed_features", "")
			observedIterationCount, _ = exec.Context.Get("loop_restart.iteration_count")
			return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-persist", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-persist"

	res, err := eng.run(ctx)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("FinalStatus = %v, want success", res.FinalStatus)
	}

	// Verify completed_features persisted across the loop_restart.
	if observedCompletedFeatures != "feature-1" {
		t.Errorf("completed_features = %q, want %q (should persist across restart)", observedCompletedFeatures, "feature-1")
	}

	// Verify ephemeral_state did NOT persist (not in persist_keys).
	if v := eng.Context.GetString("ephemeral_state", ""); v != "" {
		t.Errorf("ephemeral_state = %q, want empty (should not persist)", v)
	}

	// Verify loop_restart.iteration_count was injected.
	if observedIterationCount == nil {
		t.Error("loop_restart.iteration_count not found in context after restart")
	} else if count, ok := observedIterationCount.(int); !ok || count != 1 {
		t.Errorf("loop_restart.iteration_count = %v, want 1", observedIterationCount)
	}

	// Verify loop_restart.from_node was injected.
	fromNode := eng.Context.GetString("loop_restart.from_node", "")
	if fromNode != "check" {
		t.Errorf("loop_restart.from_node = %q, want %q", fromNode, "check")
	}
}

func TestLoopRestart_PersistKeysProgressEvent(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test persist keys in progress", default_max_retry=0, loop_restart_persist_keys="my_key"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var callCount atomic.Int32
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			if node.ID != "work" {
				return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
			}
			n := callCount.Add(1)
			if n == 1 {
				return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary error: dial tcp timeout"}, nil
			}
			return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	eng := &Engine{
		Graph:           g,
		Options:         RunOptions{RepoPath: repo, RunID: "test-persist-progress", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:       dot,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: backend,
	}
	eng.RunBranch = "attractor/run/test-persist-progress"

	_, err = eng.run(ctx)
	if err != nil {
		t.Fatalf("run() error: %v", err)
	}

	// Read the progress.ndjson from the base logs root and verify
	// the loop_restart event includes the persist_keys field.
	progressPath := filepath.Join(logsRoot, "progress.ndjson")
	progressBytes, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress: %v", err)
	}
	found := false
	for _, line := range strings.Split(string(progressBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}
		if event["event"] == "loop_restart" {
			if keys, ok := event["persist_keys"]; ok {
				if arr, ok := keys.([]any); ok && len(arr) == 1 {
					if arr[0] == "my_key" {
						found = true
						break
					}
				}
			}
		}
	}
	if !found {
		t.Error("expected loop_restart progress event with persist_keys=[\"my_key\"]")
	}
}
