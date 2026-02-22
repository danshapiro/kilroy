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

func TestRun_ParallelFanOutAndFanIn_FastForwardsWinner(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph P {
  graph [goal="test"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, prompt="b"]
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

	assertExists(t, filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	assertExists(t, filepath.Join(res.LogsRoot, "join", "status.json"))

	// Winner should deterministically be branch "a" (lexical tie-break).
	b, err := os.ReadFile(filepath.Join(res.LogsRoot, "join", "status.json"))
	if err != nil {
		t.Fatalf("read join status.json: %v", err)
	}
	out, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		t.Fatalf("decode join status.json: %v", err)
	}
	best, ok := out.ContextUpdates["parallel.fan_in.best_id"]
	if !ok {
		t.Fatalf("missing parallel.fan_in.best_id in context updates")
	}
	if strings.TrimSpace(strings.ToLower(fmt.Sprint(best))) != "a" {
		t.Fatalf("best branch: got %v, want a", best)
	}

	// Base + 5 node commits (start, par, a, join, exit) => 6 total.
	count := strings.TrimSpace(runCmdOut(t, repo, "git", "rev-list", "--count", res.RunBranch))
	if count != "6" {
		t.Fatalf("commit count: got %s, want 6 (base+5 nodes on winning path)", count)
	}
}

func TestRun_ParallelFanOut_Component_ConvergesOnBoxJoinWithoutFastForward(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph P {
  graph [goal="test box join"]
  start [shape=Mdiamond]
  par [shape=component]
  a [shape=parallelogram, tool_command="echo a > a.txt; exit 0"]
  b [shape=parallelogram, tool_command="echo b > b.txt; exit 0"]
  synth [shape=parallelogram, tool_command="echo synth > synth.txt; exit 0"]
  exit [shape=Msquare]

  start -> par
  par -> a
  par -> b
  a -> synth
  b -> synth
  synth -> exit
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

	// Parallel fan-out results should exist for the explicit component node.
	resultsPath := filepath.Join(res.LogsRoot, "par", "parallel_results.json")
	assertExists(t, resultsPath)

	// The join node should run on the main worktree, without fast-forwarding branch changes.
	files := runCmdOut(t, repo, "git", "ls-tree", "-r", "--name-only", res.FinalCommitSHA)
	if !strings.Contains(files, "synth.txt") {
		t.Fatalf("missing synth.txt in final commit; files:\n%s", files)
	}
	if strings.Contains(files, "a.txt") || strings.Contains(files, "b.txt") {
		t.Fatalf("unexpected branch artifacts in final commit (should not fast-forward):\n%s", files)
	}
	if got := strings.TrimSpace(runCmdOut(t, repo, "git", "show", res.FinalCommitSHA+":synth.txt")); got != "synth" {
		t.Fatalf("synth.txt: got %q want %q", got, "synth")
	}

	// Branch artifacts should exist in their isolated branch worktrees.
	b, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("read parallel_results.json: %v", err)
	}
	var results []parallelBranchResult
	if err := json.Unmarshal(b, &results); err != nil {
		t.Fatalf("unmarshal parallel_results.json: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 branch results, got %d", len(results))
	}

	seenA := false
	seenB := false
	for _, r := range results {
		switch strings.TrimSpace(r.BranchKey) {
		case "a":
			seenA = true
			data, err := os.ReadFile(filepath.Join(r.WorktreeDir, "a.txt"))
			if err != nil {
				t.Fatalf("read a.txt from branch worktree: %v (worktree=%s)", err, r.WorktreeDir)
			}
			if strings.TrimSpace(string(data)) != "a" {
				t.Fatalf("a.txt contents: got %q want %q", strings.TrimSpace(string(data)), "a")
			}
		case "b":
			seenB = true
			data, err := os.ReadFile(filepath.Join(r.WorktreeDir, "b.txt"))
			if err != nil {
				t.Fatalf("read b.txt from branch worktree: %v (worktree=%s)", err, r.WorktreeDir)
			}
			if strings.TrimSpace(string(data)) != "b" {
				t.Fatalf("b.txt contents: got %q want %q", strings.TrimSpace(string(data)), "b")
			}
		}
	}
	if !seenA || !seenB {
		t.Fatalf("missing expected branch keys; seenA=%v seenB=%v results=%+v", seenA, seenB, results)
	}
}
