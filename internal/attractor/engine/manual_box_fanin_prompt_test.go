package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type promptCaptureBackend struct {
	mu      sync.Mutex
	prompts map[string]string
}

func (b *promptCaptureBackend) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	_ = ctx
	_ = exec
	b.mu.Lock()
	if b.prompts == nil {
		b.prompts = map[string]string{}
	}
	b.prompts[node.ID] = prompt
	b.mu.Unlock()
	return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
}

func TestManualBoxFanInPromptPreamble_IncludesBranchLocations(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph G {
  graph [goal="manual box fan-in handoff test"]
  start [shape=Mdiamond]
  exit [shape=Msquare]
  par [shape=component]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="branch a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="branch b"]
  merge [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="merge branch outputs"]

  start -> par
  par -> a
  par -> b
  a -> merge
  b -> merge
  merge -> exit
}
`)

	backend := &promptCaptureBackend{}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	runID := "test-manual-box-fan-in-handoff"
	logsRoot := filepath.Join(t.TempDir(), runID)
	eng := newReliabilityFixtureEngine(t, repo, logsRoot, runID, dot)
	eng.AgentBackend = backend
	_, err := eng.runLoop(ctx, "start", nil, map[string]int{}, map[string]runtime.Outcome{})
	if err != nil {
		t.Fatalf("runLoop() error: %v", err)
	}

	backend.mu.Lock()
	mergePrompt := backend.prompts["merge"]
	backend.mu.Unlock()
	if strings.TrimSpace(mergePrompt) == "" {
		t.Fatalf("missing prompt capture for merge node")
	}
	if !strings.Contains(mergePrompt, "Manual parallel fan-in handoff") {
		t.Fatalf("merge prompt missing manual fan-in preamble")
	}
	if !strings.Contains(mergePrompt, "Current worktree") {
		t.Fatalf("merge prompt missing current worktree path")
	}
	if !strings.Contains(mergePrompt, "DEFAULT MERGE STRATEGY") {
		t.Fatalf("merge prompt missing default merge strategy guidance")
	}
	if !strings.Contains(mergePrompt, "git merge --no-ff") {
		t.Fatalf("merge prompt missing git merge --no-ff as primary merge method")
	}
	if !strings.Contains(mergePrompt, "resolve it however you see fit") {
		t.Fatalf("merge prompt missing conflict resolution guidance")
	}
	if !strings.Contains(mergePrompt, "branch_key=a") || !strings.Contains(mergePrompt, "branch_key=b") {
		t.Fatalf("merge prompt missing branch keys")
	}
	if !strings.Contains(mergePrompt, "/parallel/par/") || !strings.Contains(mergePrompt, "worktree_dir=") {
		t.Fatalf("merge prompt missing branch location hints")
	}
}
