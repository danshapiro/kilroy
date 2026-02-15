package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// retryCountCheckHandler fails on the first attempt, succeeds on the second,
// and records the internal.retry_count.<node_id> context key at each attempt.
type retryCountCheckHandler struct {
	observedCounts []int // retry counts seen at each attempt
}

func (h *retryCountCheckHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)

	// Read the current retry count from context.
	key := fmt.Sprintf("internal.retry_count.%s", node.ID)
	count := 0
	if v, ok := exec.Context.Get(key); ok {
		if n, ok := v.(int); ok {
			count = n
		}
	}
	h.observedCounts = append(h.observedCounts, count)

	// Fail on first attempt, succeed on second.
	marker := filepath.Join(stageDir, "attempt_1")
	if _, err := os.Stat(marker); err != nil {
		_ = os.WriteFile(marker, []byte("1"), 0o644)
		return runtime.Outcome{
			Status:        runtime.StatusRetry,
			FailureReason: "transient: try again",
		}, nil
	}
	return runtime.Outcome{Status: runtime.StatusSuccess, Notes: "ok"}, nil
}

// TestRun_InternalRetryCountContextKey verifies that the engine sets the
// built-in context key internal.retry_count.<node_id> per spec §5.1.
// It checks:
//   - The key is 0 at the start of the first attempt.
//   - The key is 1 after one retry.
//   - The final checkpoint context snapshot contains the correct value.
func TestRun_InternalRetryCountContextKey(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	logsRoot := t.TempDir()

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  r [shape=diamond, type="retry_count_check", max_retries=2]
  exit [shape=Msquare]
  start -> r -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	handler := &retryCountCheckHandler{}

	opts := RunOptions{RepoPath: repo, RunID: "retryctx", LogsRoot: logsRoot}
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
	eng.Registry.Register("retry_count_check", handler)
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	// Verify handler observed retry counts at each attempt.
	if len(handler.observedCounts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(handler.observedCounts))
	}
	// First attempt: retry count should be 0 (no retries yet).
	if handler.observedCounts[0] != 0 {
		t.Fatalf("attempt 1 retry count: got %d want 0", handler.observedCounts[0])
	}
	// Second attempt: retry count should be 1 (one retry occurred).
	if handler.observedCounts[1] != 1 {
		t.Fatalf("attempt 2 retry count: got %d want 1", handler.observedCounts[1])
	}

	// Verify the checkpoint persisted the retry count in context.
	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	key := "internal.retry_count.r"
	v, ok := cp.ContextValues[key]
	if !ok {
		t.Fatalf("checkpoint missing context key %q", key)
	}
	// JSON round-trip turns int to float64.
	var count float64
	switch c := v.(type) {
	case float64:
		count = c
	case int:
		count = float64(c)
	default:
		t.Fatalf("unexpected type for %q: %T", key, v)
	}
	// After a successful retry, retries[node.ID] is reset to 0 (success path).
	// But the context key was set to 1 during the retry. After success,
	// retries[node.ID] is reset to 0, which doesn't update the context key.
	// The context key reflects the retry count at the time of the last
	// executeWithRetry update: 1 (set when the retry happened).
	if count != 1 {
		t.Fatalf("checkpoint context %q: got %v want 1", key, count)
	}
}

// TestRun_InternalRetryCountContextKey_NoRetries verifies that
// internal.retry_count.<node_id> is 0 when no retries occur.
func TestRun_InternalRetryCountContextKey_NoRetries(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  t [
    shape=parallelogram,
    tool_command="echo ok"
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

	// Verify the checkpoint has internal.retry_count.t = 0.
	cp, err := runtime.LoadCheckpoint(filepath.Join(res.LogsRoot, "checkpoint.json"))
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	key := "internal.retry_count.t"
	v, ok := cp.ContextValues[key]
	if !ok {
		t.Fatalf("checkpoint missing context key %q", key)
	}
	var count float64
	switch c := v.(type) {
	case float64:
		count = c
	case int:
		count = float64(c)
	default:
		t.Fatalf("unexpected type for %q: %T", key, v)
	}
	if count != 0 {
		t.Fatalf("checkpoint context %q: got %v want 0", key, count)
	}
}
