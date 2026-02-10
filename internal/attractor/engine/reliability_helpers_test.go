package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

type canceledSubgraphFixtureResult struct {
	scheduledAfterCancel    bool
	nextNode                string
	startedNodesAfterCancel int
}

type parallelCancelFixtureResult struct {
	startedNodesAfterCancel int
}

func runStatusIngestionFixture(t *testing.T, canonical, worktree, invalid bool) (runtime.Outcome, string) {
	t.Helper()
	out, source, _ := runStatusIngestionFixtureWithLogs(t, canonical, worktree, invalid)
	return out, source
}

func runStatusIngestionFixtureWithLogs(t *testing.T, canonical, worktree, invalid bool) (runtime.Outcome, string, string) {
	t.Helper()
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)
	stageStatusPath := filepath.Join(logsRoot, "a", "status.json")

	var script strings.Builder
	script.WriteString("#!/usr/bin/env bash\n")
	script.WriteString("set -euo pipefail\n")
	if canonical {
		script.WriteString("mkdir -p " + shellQuote(filepath.Dir(stageStatusPath)) + "\n")
		script.WriteString("cat > " + shellQuote(stageStatusPath) + " <<'JSON'\n")
		script.WriteString("{\"status\":\"success\",\"notes\":\"canonical\"}\n")
		script.WriteString("JSON\n")
	}
	if invalid {
		script.WriteString("cat > status.json <<'JSON'\n")
		script.WriteString("{ this is invalid json }\n")
		script.WriteString("JSON\n")
	} else if worktree {
		script.WriteString("cat > status.json <<'JSON'\n")
		script.WriteString("{\"status\":\"fail\",\"failure_reason\":\"worktree fallback failure\"}\n")
		script.WriteString("JSON\n")
	}
	script.WriteString("echo '{\"type\":\"start\"}'\n")
	script.WriteString("echo '{\"type\":\"done\",\"text\":\"ok\"}'\n")

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(script.String()), 0o755); err != nil {
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

	dot := []byte(`
digraph G {
  graph [goal="status ingestion fixture"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="write status"]
  fix [shape=parallelogram, tool_command="echo fixed > fixed.txt"]
  start -> a
  a -> fix [condition="outcome=fail"]
  a -> exit [condition="outcome=success"]
  fix -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "status-ingestion-fixture",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
	})
	if err != nil && !invalid {
		t.Fatalf("RunWithConfig(status ingestion fixture): %v", err)
	}
	worktreeStatusPath := ""
	if res != nil && strings.TrimSpace(res.WorktreeDir) != "" {
		worktreeStatusPath = filepath.Join(res.WorktreeDir, "status.json")
	}
	fallbackCopied := (worktree || invalid) && worktreeStatusPath != "" && !fileExists(worktreeStatusPath)

	out, decErr := readFixtureOutcome(filepath.Join(logsRoot, "a", "status.json"))
	if decErr != nil {
		if fallbackCopied {
			return runtime.Outcome{}, string(statusSourceWorktree), logsRoot
		}
		return runtime.Outcome{}, string(statusSourceNone), logsRoot
	}

	source := string(statusSourceNone)
	switch {
	case canonical && out.Status == runtime.StatusSuccess:
		source = string(statusSourceCanonical)
	case fallbackCopied:
		source = string(statusSourceWorktree)
	}
	return out, source, logsRoot
}

func runHeartbeatFixture(t *testing.T) []map[string]any {
	t.Helper()
	cleanupStrayEngineArtifacts(t)
	t.Cleanup(func() { cleanupStrayEngineArtifacts(t) })

	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}'
sleep 2
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "200ms")
	t.Setenv("KILROY_CODEX_IDLE_TIMEOUT", "10s")

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

	dot := []byte(`
digraph G {
  graph [goal="heartbeat fixture"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="heartbeat test"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := RunWithConfig(ctx, dot, cfg, RunOptions{
		RunID:         "heartbeat-fixture",
		LogsRoot:      logsRoot,
		AllowTestShim: true,
	}); err != nil {
		t.Fatalf("RunWithConfig(heartbeat fixture): %v", err)
	}

	return readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
}

func runParallelWatchdogFixture(t *testing.T, stallTimeout time.Duration) error {
	t.Helper()
	_, err := runParallelWatchdogFixtureWithLogs(t, stallTimeout)
	return err
}

func runParallelWatchdogFixtureWithLogs(t *testing.T, stallTimeout time.Duration) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("runParallelWatchdogFixture requires sleep binary")
	}

	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	dot := []byte(`
digraph G {
  graph [goal="parallel watchdog fixture"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=parallelogram, tool_command="sleep 1"]
  b [shape=parallelogram, tool_command="sleep 1"]
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

	_, err := Run(context.Background(), dot, RunOptions{
		RepoPath:           repo,
		RunID:              "parallel-watchdog-fixture",
		LogsRoot:           logsRoot,
		StallTimeout:       stallTimeout,
		StallCheckInterval: 25 * time.Millisecond,
	})
	return logsRoot, err
}

func runCanceledSubgraphFixture(t *testing.T) canceledSubgraphFixtureResult {
	t.Helper()
	got, _, _ := runCanceledSubgraphFixtureWithLogs(t)
	return got
}

func runCanceledSubgraphFixtureWithLogs(t *testing.T) (canceledSubgraphFixtureResult, string, error) {
	t.Helper()
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	dot := []byte(`
digraph G {
  graph [goal="subgraph cancel fixture"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=diamond, type="cancel_fixture"]
  b [shape=parallelogram, tool_command="echo after-cancel > after_cancel.txt"]
  start -> a
  a -> b [condition="outcome=fail"]
  b -> exit [condition="outcome=success"]
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "subgraph-cancel-fixture", dot)

	ctx, cancel := context.WithCancel(context.Background())
	eng.Registry.Register("cancel_fixture", &cancelAfterSuccessFixtureHandler{cancel: cancel})
	_, runErr := runSubgraphUntil(ctx, eng, "a", "")

	events := readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
	startedAfterCancel := 0
	for _, ev := range events {
		if strings.TrimSpace(fmt.Sprint(ev["event"])) != "stage_attempt_start" {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(ev["node_id"])) == "b" {
			startedAfterCancel++
		}
	}

	got := canceledSubgraphFixtureResult{
		scheduledAfterCancel:    startedAfterCancel > 0,
		nextNode:                "b",
		startedNodesAfterCancel: startedAfterCancel,
	}
	return got, logsRoot, runErr
}

func runParallelCancelFixture(t *testing.T) parallelCancelFixtureResult {
	t.Helper()
	got := runCanceledSubgraphFixture(t)
	return parallelCancelFixtureResult{startedNodesAfterCancel: got.startedNodesAfterCancel}
}

func runDeterministicSubgraphCycleFixture(t *testing.T, limit int) error {
	t.Helper()
	_, err := runDeterministicSubgraphCycleFixtureWithLogs(t, limit)
	return err
}

func runDeterministicSubgraphCycleFixtureWithLogs(t *testing.T, limit int) (string, error) {
	t.Helper()
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	dot := []byte(`
digraph G {
  graph [goal="subgraph cycle fixture", loop_restart_signature_limit="` + strconv.Itoa(limit) + `"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=diamond, type="det_cycle_fixture"]
  b [shape=diamond, type="det_cycle_fixture"]
  start -> a
  a -> b [condition="outcome=fail"]
  b -> a [condition="outcome=fail"]
  a -> exit [condition="outcome=success"]
  b -> exit [condition="outcome=success"]
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "subgraph-cycle-fixture", dot)
	eng.Registry.Register("det_cycle_fixture", &deterministicCycleFixtureHandler{maxFailCalls: limit + 8})

	_, err := runSubgraphUntil(context.Background(), eng, "a", "")
	return logsRoot, err
}

func runCanceledCycleFixture(t *testing.T) error {
	t.Helper()
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	dot := []byte(`
digraph G {
  graph [goal="subgraph canceled cycle fixture", loop_restart_signature_limit="2"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  a [shape=diamond, type="canceled_cycle_fixture"]
  b [shape=diamond, type="canceled_cycle_fixture"]
  start -> a
  a -> b [condition="outcome=fail"]
  b -> a [condition="outcome=fail"]
  a -> exit [condition="outcome=success"]
  b -> exit [condition="outcome=success"]
}
`)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, "subgraph-canceled-cycle-fixture", dot)
	eng.Registry.Register("canceled_cycle_fixture", &canceledCycleFixtureHandler{maxFailCalls: 4})

	_, err := runSubgraphUntil(context.Background(), eng, "a", "")
	return err
}

func runStatusIngestionProgressFixture(t *testing.T) []map[string]any {
	t.Helper()
	return runProgressFixtureByScenario(t, "status_ingestion")
}

func runSubgraphCycleProgressFixture(t *testing.T) []map[string]any {
	t.Helper()
	return runProgressFixtureByScenario(t, "subgraph_cycle")
}

func runSubgraphCancelProgressFixture(t *testing.T) []map[string]any {
	t.Helper()
	return runProgressFixtureByScenario(t, "subgraph_cancel")
}

func runProgressFixtureByScenario(t *testing.T, scenario string) []map[string]any {
	t.Helper()

	switch strings.TrimSpace(strings.ToLower(scenario)) {
	case "status_ingestion":
		_, _, logsRoot := runStatusIngestionFixtureWithLogs(t, false, true, false)
		return readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
	case "subgraph_cycle":
		logsRoot, _ := runDeterministicSubgraphCycleFixtureWithLogs(t, 2)
		return readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
	case "subgraph_cancel":
		_, logsRoot, _ := runCanceledSubgraphFixtureWithLogs(t)
		return readFixtureProgressEvents(t, filepath.Join(logsRoot, "progress.ndjson"))
	default:
		t.Fatalf("unknown progress fixture scenario: %q", scenario)
		return nil
	}
}

func hasEvent(events []map[string]any, eventName string) bool {
	want := strings.TrimSpace(eventName)
	for _, ev := range events {
		if strings.TrimSpace(fmt.Sprint(ev["event"])) == want {
			return true
		}
	}
	return false
}

func findEventIndex(events []map[string]any, eventName, nodeID string) int {
	wantEvent := strings.TrimSpace(eventName)
	wantNode := strings.TrimSpace(nodeID)
	for i, ev := range events {
		if strings.TrimSpace(fmt.Sprint(ev["event"])) != wantEvent {
			continue
		}
		if strings.TrimSpace(fmt.Sprint(ev["node_id"])) != wantNode {
			continue
		}
		return i
	}
	return -1
}

func readFixtureOutcome(path string) (runtime.Outcome, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return runtime.Outcome{}, err
	}
	return runtime.DecodeOutcomeJSON(b)
}

func readFixtureProgressEvents(t *testing.T, progressPath string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(progressPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read progress %s: %v", progressPath, err)
	}
	lines := strings.Split(string(b), "\n")
	events := make([]map[string]any, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("decode progress row %q: %v", line, err)
		}
		events = append(events, ev)
	}
	return events
}

func newReliabilityFixtureEngine(t *testing.T, repo, logsRoot, runID string, dotSource []byte) *Engine {
	t.Helper()
	g, _, err := Prepare(dotSource)
	if err != nil {
		t.Fatalf("Prepare fixture graph: %v", err)
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		t.Fatalf("mkdir logs root: %v", err)
	}

	opts := RunOptions{
		RepoPath:        repo,
		RunID:           runID,
		LogsRoot:        logsRoot,
		WorktreeDir:     repo,
		RunBranchPrefix: "attractor/run",
	}
	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       dotSource,
		LogsRoot:        logsRoot,
		baseLogsRoot:    logsRoot,
		WorktreeDir:     repo,
		RunBranch:       opts.RunBranchPrefix + "/" + runID,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	for k, v := range g.Attrs {
		eng.Context.Set("graph."+k, v)
	}
	if goal := strings.TrimSpace(g.Attrs["goal"]); goal != "" {
		eng.Context.Set("graph.goal", goal)
	}
	return eng
}

type cancelAfterSuccessFixtureHandler struct {
	cancel context.CancelFunc
}

func (h *cancelAfterSuccessFixtureHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	if h.cancel != nil {
		h.cancel()
	}
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "fixture requested cancellation after success"}, nil
}

type deterministicCycleFixtureHandler struct {
	calls        int
	maxFailCalls int
}

type canceledCycleFixtureHandler struct {
	calls        int
	maxFailCalls int
}

func (h *canceledCycleFixtureHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	h.calls++
	if h.calls > h.maxFailCalls {
		return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "fixture canceled-cycle stop"}, nil
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "operator canceled",
		ContextUpdates: map[string]any{
			"failure_class": "canceled",
		},
	}, nil
}

func (h *deterministicCycleFixtureHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	h.calls++
	if h.calls > h.maxFailCalls {
		return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "fixture cycle stop"}, nil
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "provider auth expired",
		Meta: map[string]any{
			"failure_class": failureClassDeterministic,
		},
		ContextUpdates: map[string]any{
			"failure_class": failureClassDeterministic,
		},
	}, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func fileExists(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}
