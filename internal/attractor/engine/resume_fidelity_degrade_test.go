package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestResume_DegradesFirstResumedNodeAfterFullFidelityHop(t *testing.T) {
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

  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, fidelity=full, prompt="a"]
  b [shape=box, llm_provider=openai, llm_model=gpt-5.2, fidelity=full, prompt="b"]
  c [shape=box, llm_provider=openai, llm_model=gpt-5.2, fidelity=full, prompt="c"]

  start -> a -> b -> c -> exit
}
`)

	logsRoot := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunID: "fid", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Find the checkpoint commit for node "a" on the run branch.
	logOut := runCmdOut(t, repo, "git", "log", "--format=%H:%s", res.RunBranch)
	shaA := ""
	for _, line := range strings.Split(strings.TrimSpace(logOut), "\n") {
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		sha := strings.TrimSpace(parts[0])
		subj := strings.TrimSpace(parts[1])
		if strings.Contains(subj, "): a (") {
			shaA = sha
			break
		}
	}
	if shaA == "" {
		t.Fatalf("could not find node a commit sha in log:\n%s", logOut)
	}

	// Rewrite checkpoint.json to simulate an interrupted run after completing node "a".
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "a"
	cp.CompletedNodes = []string{"start", "a"}
	cp.GitCommitSHA = shaA
	cp.ContextValues = map[string]any{}
	cp.Logs = []string{}
	if cp.Extra == nil {
		cp.Extra = map[string]any{}
	}
	cp.Extra["last_fidelity"] = "full"
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	// Resume should degrade the first resumed node (b) to summary:high, but allow subsequent nodes (c)
	// to use full fidelity again.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel2()
	if _, err := Resume(ctx2, res.LogsRoot); err != nil {
		t.Fatalf("Resume: %v", err)
	}

	bPrompt, err := os.ReadFile(filepath.Join(res.LogsRoot, "b", "prompt.md"))
	if err != nil {
		t.Fatalf("read b/prompt.md: %v", err)
	}
	if !strings.Contains(string(bPrompt), "Fidelity: summary:high") {
		t.Fatalf("expected b to be downgraded to summary:high; prompt:\n%s", string(bPrompt))
	}

	cPrompt, err := os.ReadFile(filepath.Join(res.LogsRoot, "c", "prompt.md"))
	if err != nil {
		t.Fatalf("read c/prompt.md: %v", err)
	}
	if strings.Contains(string(cPrompt), "Kilroy Context") {
		t.Fatalf("expected c to use full fidelity (no synthesized preamble); prompt:\n%s", string(cPrompt))
	}
}
