package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

// TestResume_ImplicitFanOut_DispatchesParallelBranches verifies that the resume
// path mirrors the forward path's implicit fan-out when a node has multiple
// eligible outgoing edges that converge at a common downstream node.
func TestResume_ImplicitFanOut_DispatchesParallelBranches(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dotSrc := []byte(`
digraph G {
  graph [goal="test implicit fan-out resume"]
  start  [shape=Mdiamond]
  source [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="source"]
  branch_a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  branch_b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
  synth [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="synth"]
  exit  [shape=Msquare]

  start -> source
  source -> branch_a
  source -> branch_b
  branch_a -> synth
  branch_b -> synth
  synth -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Run to completion first so we have valid artifacts/checkpoint.
	res, err := Run(ctx, dotSrc, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("initial run final status: got %q want %q", res.FinalStatus, runtime.FinalSuccess)
	}

	// Find the git SHA for the "source" node commit.
	log := runCmdOut(t, repo, "git", "log", "--format=%H:%s", res.RunBranch)
	sourceSHA := ""
	for _, line := range strings.Split(log, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		msg := strings.TrimSpace(parts[1])
		if strings.Contains(msg, "source (") {
			sourceSHA = strings.TrimSpace(parts[0])
			break
		}
	}
	if sourceSHA == "" {
		t.Fatalf("could not find commit for node source in log:\n%s", log)
	}

	// Remove the parallel_results.json from the initial run so we can verify
	// that resume re-creates it.
	_ = os.Remove(filepath.Join(res.LogsRoot, "source", "parallel_results.json"))

	// Rewrite checkpoint to simulate a crash right after "source" completed
	// but before implicit fan-out dispatched.
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "source"
	cp.CompletedNodes = []string{"start", "source"}
	cp.GitCommitSHA = sourceSHA
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	// Resume should dispatch implicit fan-out (branch_a, branch_b → synth) and succeed.
	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	if res2.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("resume final status: got %q want %q", res2.FinalStatus, runtime.FinalSuccess)
	}

	// Verify that parallel_results.json was re-created by the resume path.
	resultsPath := filepath.Join(res.LogsRoot, "source", "parallel_results.json")
	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("parallel_results.json not created by resume: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(b, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 branch results, got %d", len(results))
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
}
