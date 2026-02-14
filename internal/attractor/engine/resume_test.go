package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestResume_EngineOptionsAreFullyHydrated(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	dot := []byte(`
digraph P {
  graph [goal="hydrate"]
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
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo, RunBranchPrefix: "custom/prefix"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "start"
	cp.CompletedNodes = []string{"start"}
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume: %v", err)
	}

	wantBranch := "custom/prefix/" + res.RunID
	if strings.TrimSpace(res2.RunBranch) != wantBranch {
		t.Fatalf("resumed run branch: got %q want %q", res2.RunBranch, wantBranch)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if !filepath.IsAbs(strings.TrimSpace(anyToString(manifest["logs_root"]))) {
		t.Fatalf("manifest logs_root should be absolute, got %q", anyToString(manifest["logs_root"]))
	}
	if !filepath.IsAbs(strings.TrimSpace(anyToString(manifest["worktree"]))) {
		t.Fatalf("manifest worktree should be absolute, got %q", anyToString(manifest["worktree"]))
	}

	latestCP, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint latest: %v", err)
	}
	if strings.TrimSpace(anyToString(latestCP.Extra["base_logs_root"])) == "" {
		t.Fatalf("checkpoint extra missing base_logs_root")
	}

	resultsBytes, err := os.ReadFile(filepath.Join(res.LogsRoot, "par", "parallel_results.json"))
	if err != nil {
		t.Fatalf("read parallel results: %v", err)
	}
	var results []map[string]any
	if err := json.Unmarshal(resultsBytes, &results); err != nil {
		t.Fatalf("unmarshal parallel results: %v", err)
	}
	for _, row := range results {
		branchName := strings.TrimSpace(anyToString(row["branch_name"]))
		if !strings.HasPrefix(branchName, "custom/prefix/parallel/") {
			t.Fatalf("parallel branch not hydrated from options: %q", branchName)
		}
	}
}

func TestResume_FromCheckpoint_RewindsBranchAndContinues(t *testing.T) {
	// Keep logs under the test tempdir so ResumeFromBranch/guessing is deterministic.
	t.Setenv("XDG_STATE_HOME", t.TempDir())

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
  a [shape=parallelogram, tool_command="echo hi > foo.txt"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	// Find the checkpoint commit for node "a".
	log := runCmdOut(t, repo, "git", "log", "--format=%H:%s", res.RunBranch)
	wantMsgPrefix := "attractor(" + res.RunID + "): a ("
	aSHA := ""
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
		if strings.HasPrefix(msg, wantMsgPrefix) {
			aSHA = strings.TrimSpace(parts[0])
			break
		}
	}
	if aSHA == "" {
		t.Fatalf("could not find commit for node a in log:\n%s", log)
	}

	// Rewrite checkpoint.json to simulate a crash after node a completed.
	cpPath := filepath.Join(res.LogsRoot, "checkpoint.json")
	cp, err := runtime.LoadCheckpoint(cpPath)
	if err != nil {
		t.Fatalf("LoadCheckpoint: %v", err)
	}
	cp.CurrentNode = "a"
	cp.CompletedNodes = []string{"start", "a"}
	cp.GitCommitSHA = aSHA
	if err := cp.Save(cpPath); err != nil {
		t.Fatalf("Save checkpoint: %v", err)
	}

	// Resume should reset the branch to aSHA and re-run exit.
	res2, err := Resume(ctx, res.LogsRoot)
	if err != nil {
		t.Fatalf("Resume() error: %v", err)
	}
	if res2.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("final status: got %q want %q", res2.FinalStatus, runtime.FinalSuccess)
	}

	// Ensure the branch has base+start+a+exit commits after resume.
	count := strings.TrimSpace(runCmdOut(t, repo, "git", "rev-list", "--count", res.RunBranch))
	if count != "4" {
		t.Fatalf("commit count after resume: got %s want 4", count)
	}
}

func TestResumeFromBranch_FindsLogsRootAndReturnsResult(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

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
  a [shape=parallelogram, tool_command="echo hi > foo.txt"]
  start -> a -> exit
}
`)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := Run(ctx, dot, RunOptions{RepoPath: repo})
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}

	res2, err := ResumeFromBranch(ctx, repo, res.RunBranch)
	if err != nil {
		t.Fatalf("ResumeFromBranch: %v", err)
	}
	if res2.RunID != res.RunID {
		t.Fatalf("run_id: got %q want %q", res2.RunID, res.RunID)
	}
}

func TestNewResumeCodergenBackend_LoadsProviderRuntimes(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				Protocol:      "anthropic_messages",
				APIKeyEnv:     "KIMI_API_KEY",
				BaseURL:       "https://api.kimi.com/coding",
				Path:          "/v1/messages",
				ProfileFamily: "openai",
			},
		},
		"zai": {
			Backend: BackendAPI,
			API: ProviderAPIConfig{
				Protocol:      "openai_chat_completions",
				APIKeyEnv:     "ZAI_API_KEY",
				BaseURL:       "https://api.z.ai",
				Path:          "/api/coding/paas/v4/chat/completions",
				ProfileFamily: "openai",
			},
		},
	}

	backend, err := newResumeCodergenBackend(cfg, nil)
	if err != nil {
		t.Fatalf("newResumeCodergenBackend: %v", err)
	}
	router, ok := backend.(*CodergenRouter)
	if !ok {
		t.Fatalf("backend type: got %T want *CodergenRouter", backend)
	}
	if _, ok := router.providerRuntimes["kimi"]; !ok {
		t.Fatalf("missing provider runtime for kimi")
	}
	if _, ok := router.providerRuntimes["zai"]; !ok {
		t.Fatalf("missing provider runtime for zai")
	}

	kimi := router.providerRuntimes["kimi"]
	profile, err := profileForRuntimeProvider(kimi, "kimi-k2.5")
	if err != nil {
		t.Fatalf("profileForRuntimeProvider(kimi): %v", err)
	}
	if profile.ID() != "kimi" {
		t.Fatalf("profile ID: got %q want %q", profile.ID(), "kimi")
	}

	zai := router.providerRuntimes["zai"]
	profile, err = profileForRuntimeProvider(zai, "glm-4.7")
	if err != nil {
		t.Fatalf("profileForRuntimeProvider(zai): %v", err)
	}
	if profile.ID() != "zai" {
		t.Fatalf("profile ID: got %q want %q", profile.ID(), "zai")
	}
}
