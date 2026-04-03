package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type preferBHandler struct{}

func (h *preferBHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	// Emit a preferred_label so the downstream conditional node can select the correct edge.
	return runtime.Outcome{Status: runtime.StatusSuccess, PreferredLabel: "B"}, nil
}

func TestRun_ConditionalHandler_PassesThroughPreferredLabelForRouting(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	g, _, err := Prepare([]byte(`
digraph G {
  start [shape=Mdiamond]
  a [shape=diamond, type="prefer_b"]
  cond [shape=diamond]
  b [shape=parallelogram, tool_command="echo b > chosen.txt"]
  c [shape=parallelogram, tool_command="echo c > chosen.txt"]
  exit [shape=Msquare]
  start -> a -> cond
  cond -> b [label="B", weight=0]
  cond -> c [label="C", weight=10]
  b -> exit
  c -> exit
}
`))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	opts := RunOptions{RepoPath: repo, RunID: "cond", LogsRoot: logsRoot}
	if err := opts.applyDefaults(); err != nil {
		t.Fatalf("applyDefaults: %v", err)
	}
	eng := &Engine{
		Graph:        g,
		Options:      opts,
		DotSource:    []byte(""),
		LogsRoot:     opts.LogsRoot,
		WorktreeDir:  opts.WorktreeDir,
		Context:      runtime.NewContext(),
		Registry:     NewDefaultRegistry(),
		Interviewer:  &AutoApproveInterviewer{},
		AgentBackend: &SimulatedAgentBackend{},
	}
	eng.Registry.Register("prefer_b", &preferBHandler{})
	eng.RunBranch = "attractor/run/" + opts.RunID

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := eng.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(eng.WorktreeDir, "chosen.txt"))
	if err != nil {
		t.Fatalf("read chosen.txt: %v", err)
	}
	if got := strings.TrimSpace(string(b)); got != "b" {
		t.Fatalf("chosen.txt: got %q want %q", got, "b")
	}
}

func TestConditionalPassThrough_PreservesFailureReasonAndClass(t *testing.T) {
	ctxState := runtime.NewContext()
	ctxState.Set("outcome", "fail")
	ctxState.Set("preferred_label", "retry")
	ctxState.Set("failure_reason", "provider timeout")
	ctxState.Set("failure_class", "transient_infra")

	out, err := (&ConditionalHandler{}).Execute(context.Background(), &Execution{
		Context: ctxState,
	}, model.NewNode("cond"))
	if err != nil {
		t.Fatalf("ConditionalHandler.Execute: %v", err)
	}
	if out.FailureReason != "provider timeout" {
		t.Fatalf("failure_reason=%q want %q", out.FailureReason, "provider timeout")
	}
	if got := strings.TrimSpace(anyToString(out.ContextUpdates["failure_class"])); got != failureClassTransientInfra {
		t.Fatalf("failure_class=%q want %q", got, failureClassTransientInfra)
	}
}

func TestConditionalPassThrough_DoesNotEmitEmptyFailureClassUpdate(t *testing.T) {
	ctxState := runtime.NewContext()
	ctxState.Set("outcome", "success")
	ctxState.Set("preferred_label", "next")

	out, err := (&ConditionalHandler{}).Execute(context.Background(), &Execution{
		Context: ctxState,
	}, model.NewNode("cond"))
	if err != nil {
		t.Fatalf("ConditionalHandler.Execute: %v", err)
	}
	if out.ContextUpdates == nil {
		return
	}
	if _, ok := out.ContextUpdates["failure_class"]; ok {
		t.Fatalf("unexpected failure_class update on success path: %v", out.ContextUpdates["failure_class"])
	}
}
