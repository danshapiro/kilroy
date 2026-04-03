package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func TestGitPushIfConfigured_NoPushRemote(t *testing.T) {
	eng := &Engine{
		RunConfig: &RunConfigFile{},
	}
	// Should be a no-op (no panic, no error).
	eng.gitPushIfConfigured()
}

func TestGitPushIfConfigured_NilRunConfig(t *testing.T) {
	eng := &Engine{}
	eng.gitPushIfConfigured()
}

func TestGitPushIfConfigured_NilEngine(t *testing.T) {
	var eng *Engine
	eng.gitPushIfConfigured()
}

func TestGitPushIfConfigured_PushesToRemote(t *testing.T) {
	// Set up a source repo with one commit.
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Create a branch to push.
	runCmd(t, repo, "git", "branch", "attractor/run/test-push", "HEAD")

	// Set up a bare remote.
	bare := t.TempDir()
	runCmd(t, bare, "git", "init", "--bare")
	runCmd(t, repo, "git", "remote", "add", "test-remote", bare)

	eng := &Engine{
		Options:   RunOptions{RepoPath: repo},
		RunBranch: "attractor/run/test-push",
		RunConfig: &RunConfigFile{},
	}
	eng.RunConfig.Git.PushRemote = "test-remote"

	eng.gitPushIfConfigured()

	// Verify the branch exists on the remote.
	out := runCmdOut(t, bare, "git", "branch", "--list", "attractor/run/test-push")
	if !strings.Contains(out, "attractor/run/test-push") {
		t.Fatalf("expected branch on remote, got: %q", out)
	}
}

func TestLoopRestart_PushesOnRestart(t *testing.T) {
	repo := t.TempDir()
	runCmd(t, repo, "git", "init")
	runCmd(t, repo, "git", "config", "user.name", "tester")
	runCmd(t, repo, "git", "config", "user.email", "tester@example.com")
	_ = os.WriteFile(filepath.Join(repo, "README.md"), []byte("hello\n"), 0o644)
	runCmd(t, repo, "git", "add", "-A")
	runCmd(t, repo, "git", "commit", "-m", "init")

	// Bare remote to receive pushes.
	bare := t.TempDir()
	runCmd(t, bare, "git", "init", "--bare")
	runCmd(t, repo, "git", "remote", "add", "test-remote", bare)

	dot := []byte(`
digraph G {
  graph [goal="test push on restart"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  work  [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="do work"]
  check [shape=diamond]
  start -> work
  work -> check
  check -> exit [condition="outcome=success"]
  check -> work [condition="outcome=fail", loop_restart=true]
  check -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	var callCount atomic.Int32
	backend := &countingBackend{
		fn: func(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
			n := callCount.Add(1)
			if node.ID == "work" && n == 1 {
				return "fail", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: "transient_infra: connection reset"}, nil
			}
			return "ok", &runtime.Outcome{Status: runtime.StatusSuccess}, nil
		},
	}

	g, _, err := Prepare(dot)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}

	logsRoot := t.TempDir()
	cfg := &RunConfigFile{}
	cfg.Version = 1
	cfg.Git.PushRemote = "test-remote"

	eng := &Engine{
		Graph:        g,
		Options:      RunOptions{RepoPath: repo, RunID: "test-push-restart", LogsRoot: logsRoot, WorktreeDir: filepath.Join(logsRoot, "worktree"), RunBranchPrefix: "attractor/run", RequireClean: true},
		DotSource:    dot,
		LogsRoot:     logsRoot,
		WorktreeDir:  filepath.Join(logsRoot, "worktree"),
		Context:      runtime.NewContext(),
		Registry:     NewDefaultRegistry(),
		Interviewer:  &AutoApproveInterviewer{},
		AgentBackend: backend,
		RunConfig:    cfg,
	}
	eng.RunBranch = "attractor/run/test-push-restart"

	res, err := eng.run(ctx)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.FinalStatus != runtime.FinalSuccess {
		t.Fatalf("expected final status success, got %s", res.FinalStatus)
	}

	// Verify the branch was pushed to the remote.
	out := runCmdOut(t, bare, "git", "branch", "--list", "attractor/run/test-push-restart")
	if !strings.Contains(out, "attractor/run/test-push-restart") {
		t.Fatalf("expected branch on remote after loop_restart, got: %q", out)
	}
}
