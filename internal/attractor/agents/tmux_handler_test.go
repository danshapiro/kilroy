// Integration tests for TmuxAgentHandler running graphs through the engine.
package agents

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/templates"
	"github.com/danshapiro/kilroy/internal/attractor/agents/tmux"
	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const testSocket = "kilroy-agent-test"

// writeFakeAgent creates a shell script that simulates an agent tool.
// It reads args, does work in the worktree, writes status.json, and exits.
func writeFakeAgent(t *testing.T, dir string, output string, exitCode int) string {
	t.Helper()
	script := filepath.Join(dir, "fake-agent")
	content := fmt.Sprintf(`#!/bin/bash
# Fake agent: writes output and exits
echo "%s"
exit %d
`, output, exitCode)
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	return script
}

// writeFakeAgentWithWork creates a script that modifies the working directory.
func writeFakeAgentWithWork(t *testing.T, dir string, filename, content string) string {
	t.Helper()
	script := filepath.Join(dir, "fake-agent-work")
	scriptContent := fmt.Sprintf(`#!/bin/bash
# Fake agent: does work in cwd
echo '%s' > '%s'
echo "Agent completed: wrote %s"
exit 0
`, content, filename, filename)
	if err := os.WriteFile(script, []byte(scriptContent), 0o755); err != nil {
		t.Fatalf("write fake agent: %v", err)
	}
	return script
}

func fakeAgentTemplate(scriptPath string) templates.Template {
	return templates.Template{
		Name:   "fake",
		Binary: scriptPath,
		BuildArgs: func(prompt, workDir string) []string {
			return []string{prompt}
		},
		BuildEnv: func() map[string]string {
			return nil
		},
		ExitsOnComplete: true,
		StartupTimeout:  5 * time.Second,
	}
}

func TestTmuxAgentHandler_FakeAgent_SuccessfulExecution(t *testing.T) {
	scriptDir := t.TempDir()
	script := writeFakeAgent(t, scriptDir, "FAKE_AGENT_OUTPUT_OK", 0)

	reg := templates.DefaultRegistry()
	reg.Register(fakeAgentTemplate(script))

	mgr := tmux.NewManager(testSocket)
	defer exec.Command("tmux", "-u", "-L", testSocket, "kill-server").Run()

	handler := &TmuxAgentHandler{
		Tmux:      mgr,
		Templates: reg,
		Timeout:   30 * time.Second,
	}

	// Create minimal execution context.
	logsRoot := t.TempDir()
	workDir := t.TempDir()

	node := model.NewNode("test_node")
	node.Attrs["agent_tool"] = "fake"
	node.Attrs["prompt"] = "do something"

	exec := &engine.Execution{
		Graph:       model.NewGraph("test"),
		Context:     runtime.NewContext(),
		LogsRoot:    logsRoot,
		WorktreeDir: workDir,
		Engine: &engine.Engine{
			Options: engine.RunOptions{RunID: "test-run-001"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := handler.Execute(ctx, exec, node)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != runtime.StatusSuccess {
		t.Fatalf("status = %q, want success (reason: %s)", outcome.Status, outcome.FailureReason)
	}

	// Verify response.md was written.
	respPath := filepath.Join(logsRoot, "test_node", "response.md")
	resp, err := os.ReadFile(respPath)
	if err != nil {
		t.Fatalf("read response.md: %v", err)
	}
	if !strings.Contains(string(resp), "FAKE_AGENT_OUTPUT_OK") {
		t.Fatalf("response = %q, want to contain FAKE_AGENT_OUTPUT_OK", string(resp))
	}

	// Verify prompt.md was written.
	promptPath := filepath.Join(logsRoot, "test_node", "prompt.md")
	prompt, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt.md: %v", err)
	}
	if !strings.Contains(string(prompt), "do something") {
		t.Fatalf("prompt = %q, want 'do something'", string(prompt))
	}
}

func TestTmuxAgentHandler_FakeAgent_FailedExecution(t *testing.T) {
	scriptDir := t.TempDir()
	script := writeFakeAgent(t, scriptDir, "ERROR: something went wrong", 1)

	reg := templates.DefaultRegistry()
	reg.Register(fakeAgentTemplate(script))

	mgr := tmux.NewManager(testSocket)
	defer exec.Command("tmux", "-u", "-L", testSocket, "kill-server").Run()

	handler := &TmuxAgentHandler{
		Tmux:      mgr,
		Templates: reg,
		Timeout:   30 * time.Second,
	}

	logsRoot := t.TempDir()
	workDir := t.TempDir()

	node := model.NewNode("fail_node")
	node.Attrs["agent_tool"] = "fake"
	node.Attrs["prompt"] = "do something that fails"

	execCtx := &engine.Execution{
		Graph:       model.NewGraph("test"),
		Context:     runtime.NewContext(),
		LogsRoot:    logsRoot,
		WorktreeDir: workDir,
		Engine: &engine.Engine{
			Options: engine.RunOptions{RunID: "test-run-002"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := handler.Execute(ctx, execCtx, node)
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if outcome.Status != runtime.StatusFail {
		t.Fatalf("status = %q, want fail (agent exited with code 1)", outcome.Status)
	}
	if !strings.Contains(outcome.FailureReason, "code 1") {
		t.Fatalf("failure_reason = %q, want mention of exit code 1", outcome.FailureReason)
	}
	t.Logf("correctly detected failure: %s", outcome.FailureReason)
}

func TestTmuxAgentHandler_FakeAgent_WorksInWorkDir(t *testing.T) {
	scriptDir := t.TempDir()
	script := writeFakeAgentWithWork(t, scriptDir, "agent-output.txt", "hello from agent")

	reg := templates.DefaultRegistry()
	reg.Register(fakeAgentTemplate(script))

	mgr := tmux.NewManager(testSocket)
	defer exec.Command("tmux", "-u", "-L", testSocket, "kill-server").Run()

	handler := &TmuxAgentHandler{
		Tmux:      mgr,
		Templates: reg,
		Timeout:   30 * time.Second,
	}

	logsRoot := t.TempDir()
	workDir := t.TempDir()

	node := model.NewNode("work_node")
	node.Attrs["agent_tool"] = "fake"
	node.Attrs["prompt"] = "create a file"

	execCtx := &engine.Execution{
		Graph:       model.NewGraph("test"),
		Context:     runtime.NewContext(),
		LogsRoot:    logsRoot,
		WorktreeDir: workDir,
		Engine: &engine.Engine{
			Options: engine.RunOptions{RunID: "test-run-003"},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	outcome, err := handler.Execute(ctx, execCtx, node)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if outcome.Status != runtime.StatusSuccess {
		t.Fatalf("status = %q, want success", outcome.Status)
	}

	// Verify the agent created a file in the work directory.
	outputPath := filepath.Join(workDir, "agent-output.txt")
	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("agent didn't create file in workdir: %v", err)
	}
	if !strings.Contains(string(data), "hello from agent") {
		t.Fatalf("file content = %q, want 'hello from agent'", string(data))
	}
}
