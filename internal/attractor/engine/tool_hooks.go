package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/agent"
	"github.com/danshapiro/kilroy/internal/attractor/model"
)

// resolveToolHook resolves a tool hook command from node attrs, then graph attrs.
// Returns empty string if no hook is configured.
func resolveToolHook(node *model.Node, graph *model.Graph, hookKey string) string {
	if node != nil {
		if cmd := strings.TrimSpace(node.Attr(hookKey, "")); cmd != "" {
			return cmd
		}
	}
	if graph != nil {
		if cmd := strings.TrimSpace(graph.Attrs[hookKey]); cmd != "" {
			return cmd
		}
	}
	return ""
}

// toolHookEnv builds environment variables for a tool hook invocation.
func toolHookEnv(base []string, nodeID, toolName, callID string) []string {
	env := append([]string{}, base...)
	env = append(env,
		"KILROY_NODE_ID="+nodeID,
		"KILROY_TOOL_NAME="+toolName,
		"KILROY_CALL_ID="+callID,
	)
	return env
}

// runToolHook executes a tool hook shell command. Returns (exitCode, error).
// For pre-hooks: exit 0 = proceed, non-zero = skip the tool call.
// For post-hooks: exit code is logged but does not block.
func runToolHook(ctx context.Context, hookCmd string, worktreeDir string, env []string, stdinJSON string, stageDir string, hookType string, callID string) (int, error) {
	if strings.TrimSpace(hookCmd) == "" {
		return 0, nil
	}
	timeout := 30 * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cctx, "bash", "-c", hookCmd)
	if worktreeDir != "" {
		cmd.Dir = worktreeDir
	}
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdinJSON)

	var stdoutBuf, stderrBuf strings.Builder
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	runErr := cmd.Run()
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	// Best-effort: log hook result to stage directory.
	if stageDir != "" {
		logName := fmt.Sprintf("tool_hook_%s_%s.json", hookType, sanitizeHookCallID(callID))
		_ = writeJSON(filepath.Join(stageDir, logName), map[string]any{
			"hook_type": hookType,
			"hook_cmd":  hookCmd,
			"call_id":   callID,
			"exit_code": exitCode,
			"stdout":    truncate(stdoutBuf.String(), 4000),
			"stderr":    truncate(stderrBuf.String(), 4000),
			"timed_out": cctx.Err() == context.DeadlineExceeded,
		})
	}

	if runErr != nil {
		return exitCode, runErr
	}
	return exitCode, nil
}

// buildToolHookStdinJSON creates the JSON payload for tool hook stdin.
func buildToolHookStdinJSON(toolName, callID, argsJSON, resultOutput string, isError bool, hookType string) string {
	data := map[string]any{
		"hook_type": hookType,
		"tool_name": toolName,
		"call_id":   callID,
	}
	if argsJSON != "" {
		data["arguments_json"] = argsJSON
	}
	if hookType == "post" {
		data["output"] = truncate(resultOutput, 8000)
		data["is_error"] = isError
	}
	b, err := json.Marshal(data)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// runPreToolHook runs the tool_hooks.pre shell command synchronously and returns
// a non-empty skip reason if the tool call should be skipped (pre-hook exited
// non-zero). This is called from the ToolCallFilter on the agent Session, which
// runs before the tool is actually executed.
func runPreToolHook(ctx context.Context, execCtx *Execution, node *model.Node, stageDir string, toolName, callID, argsJSON string) string {
	if execCtx == nil || execCtx.Engine == nil || node == nil {
		return ""
	}
	hookCmd := resolveToolHook(node, execCtx.Engine.Graph, "tool_hooks.pre")
	if hookCmd == "" {
		return ""
	}
	if toolName == "" || callID == "" {
		return ""
	}
	stdinJSON := buildToolHookStdinJSON(toolName, callID, argsJSON, "", false, "pre")
	env := toolHookEnv(buildBaseNodeEnv(execCtx.WorktreeDir), node.ID, toolName, callID)
	exitCode, err := runToolHook(ctx, hookCmd, execCtx.WorktreeDir, env, stdinJSON, stageDir, "pre", callID)
	if exitCode != 0 {
		reason := fmt.Sprintf("tool_hooks.pre exit %d for tool=%s call_id=%s: %v", exitCode, toolName, callID, err)
		execCtx.Engine.Warn(reason)
		execCtx.Engine.appendProgress(map[string]any{
			"event":     "tool_hook_pre_skip",
			"node_id":   node.ID,
			"tool_name": toolName,
			"call_id":   callID,
			"exit_code": exitCode,
		})
		return fmt.Sprintf("Tool call skipped by pre-hook (exit %d)", exitCode)
	}
	return ""
}

// executeToolHookForEvent is called from the agent event loop to execute
// tool_hooks.post shell commands after LLM tool calls complete.
// Pre-hooks are handled separately via runPreToolHook and the ToolCallFilter.
// This is best-effort: hook failures are logged as warnings but never block the run.
func executeToolHookForEvent(ctx context.Context, execCtx *Execution, node *model.Node, ev agent.SessionEvent, stageDir string) {
	if execCtx == nil || execCtx.Engine == nil || node == nil || ev.Data == nil {
		return
	}

	switch ev.Kind {
	case agent.EventToolCallEnd:
		hookCmd := resolveToolHook(node, execCtx.Engine.Graph, "tool_hooks.post")
		if hookCmd == "" {
			return
		}
		toolName := strings.TrimSpace(fmt.Sprint(ev.Data["tool_name"]))
		callID := strings.TrimSpace(fmt.Sprint(ev.Data["call_id"]))
		if toolName == "" || callID == "" {
			return
		}
		isErr, _ := ev.Data["is_error"].(bool)
		fullOutput := fmt.Sprint(ev.Data["full_output"])
		stdinJSON := buildToolHookStdinJSON(toolName, callID, "", fullOutput, isErr, "post")
		env := toolHookEnv(buildBaseNodeEnv(execCtx.WorktreeDir), node.ID, toolName, callID)
		exitCode, err := runToolHook(ctx, hookCmd, execCtx.WorktreeDir, env, stdinJSON, stageDir, "post", callID)
		if err != nil {
			execCtx.Engine.Warn(fmt.Sprintf("tool_hooks.post exit %d for tool=%s call_id=%s: %v", exitCode, toolName, callID, err))
		}
	}
}

// sanitizeHookCallID makes a call ID filesystem-safe for log file names.
func sanitizeHookCallID(callID string) string {
	s := strings.TrimSpace(callID)
	if len(s) > 32 {
		s = s[:32]
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := b.String()
	if out == "" {
		return "unknown"
	}
	return out
}
