package engine

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// Intentionally uses shape=parallelogram/tool_command because this is the
// existing supported ToolHandler path in the current engine.
func TestRun_GlobalStageTimeoutCapsToolNode(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit [condition="outcome=success"]
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 100 * time.Millisecond}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected stage timeout error")
	}
}

func TestRun_GlobalAndNodeTimeout_UsesSmallerTimeout(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("requires sleep binary")
	}
	dot := []byte(`digraph G {
  start [shape=Mdiamond]
  wait [shape=parallelogram, timeout="1s", tool_command="sleep 2"]
  exit [shape=Msquare]
  start -> wait
  wait -> exit [condition="outcome=success"]
}`)
	repo := initTestRepo(t)
	opts := RunOptions{RepoPath: repo, StageTimeout: 5 * time.Second}
	_, err := Run(context.Background(), dot, opts)
	if err == nil {
		t.Fatal("expected timeout from node/global min timeout")
	}
}
