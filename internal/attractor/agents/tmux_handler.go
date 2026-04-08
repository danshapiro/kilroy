// TmuxAgentHandler executes agent nodes by spawning CLI tools in tmux sessions.
// This replaces the subprocess-pipe model with observable, persistent sessions.
package agents

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/agentlog"
	"github.com/danshapiro/kilroy/internal/attractor/agents/templates"
	"github.com/danshapiro/kilroy/internal/attractor/agents/tmux"
	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const kilroySocket = "kilroy"

// TmuxAgentHandler invokes LLM CLI tools via tmux sessions.
type TmuxAgentHandler struct {
	Tmux      *tmux.Manager
	Templates *templates.Registry
	Timeout   time.Duration // default timeout per node (0 = 30 min)
}

// NewTmuxAgentHandler creates a handler with default tmux manager and templates.
func NewTmuxAgentHandler() *TmuxAgentHandler {
	return &TmuxAgentHandler{
		Tmux:      tmux.NewManager(kilroySocket),
		Templates: templates.DefaultRegistry(),
		Timeout:   30 * time.Minute,
	}
}

// UsesFidelity implements engine.FidelityAwareHandler.
func (h *TmuxAgentHandler) UsesFidelity() bool { return true }

// RequiresProvider implements engine.ProviderRequiringHandler.
func (h *TmuxAgentHandler) RequiresProvider() bool { return true }

// Execute implements engine.Handler. Spawns a CLI tool in a tmux session,
// waits for completion, captures output, and returns an outcome.
func (h *TmuxAgentHandler) Execute(ctx context.Context, exec *engine.Execution, node *model.Node) (runtime.Outcome, error) {
	// Resolve which CLI tool to use.
	toolName := resolveToolName(node)
	tmpl := h.Templates.Get(toolName)
	if tmpl == nil {
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("no invocation template for tool %q", toolName),
		}, nil
	}

	// Build prompt from node attributes.
	prompt := strings.TrimSpace(node.Prompt())
	if prompt == "" {
		prompt = node.Label()
	}

	// Session name: kilroy-{runID}-{nodeID} (unique per node execution).
	runID := ""
	if exec != nil && exec.Engine != nil {
		runID = exec.Engine.Options.RunID
	}
	sessionName := buildSessionName(runID, node.ID)

	// Build environment variables.
	env := tmpl.BuildEnv()
	if env == nil {
		env = map[string]string{}
	}
	if runID != "" {
		env["KILROY_RUN_ID"] = runID
	}
	env["KILROY_NODE_ID"] = node.ID
	// Add input env vars if available.
	if exec != nil && exec.Engine != nil {
		for k, v := range engine.InputEnvVars(exec.Engine.Options.Inputs) {
			env[k] = v
		}
	}

	// Resolve model from node attributes.
	modelID := strings.TrimSpace(node.Attr("llm_model", ""))

	// Build and write the command.
	command := tmpl.BuildCommand(prompt, exec.WorktreeDir, modelID)
	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)

	// Run per-tool session preparation (e.g. write isolated config files).
	if tmpl.PrepareSession != nil {
		if err := tmpl.PrepareSession(stageDir, env); err != nil {
			return runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: fmt.Sprintf("prepare session for %s: %v", toolName, err),
			}, nil
		}
	}
	_ = os.WriteFile(filepath.Join(stageDir, "tmux_command.txt"), []byte(command), 0o644)

	// Write prompt for debugging.
	_ = os.WriteFile(filepath.Join(stageDir, "prompt.md"), []byte(prompt), 0o644)

	// Emit progress event.
	if exec.Engine != nil {
		exec.Engine.AppendProgress(map[string]any{
			"event":   "tmux_session_start",
			"node_id": node.ID,
			"tool":    toolName,
			"session": sessionName,
		})
	}

	// Create tmux session.
	sessionStartTime := time.Now()
	session, err := h.Tmux.CreateSession(sessionName, exec.WorktreeDir, command, env)
	if err != nil {
		return runtime.Outcome{
			Status:         runtime.StatusFail,
			FailureReason:  fmt.Sprintf("create tmux session: %v", err),
			Meta:           map[string]any{"failure_class": "transient_infra"},
			ContextUpdates: map[string]any{"failure_class": "transient_infra"},
		}, nil
	}

	// Store session metadata.
	_ = h.Tmux.SetEnvironment(sessionName, "KILROY_RUN_ID", runID)
	_ = h.Tmux.SetEnvironment(sessionName, "KILROY_NODE_ID", node.ID)

	// Handle startup dialogs.
	for _, dialog := range tmpl.StartupDialogs {
		h.handleStartupDialog(session.Name, dialog, tmpl.StartupTimeout)
	}

	// Determine timeout.
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}

	// Wait for completion.
	var waitErr error
	if tmpl.ExitsOnComplete {
		waitErr = h.Tmux.WaitForExit(ctx, sessionName, timeout)
	} else {
		waitErr = h.Tmux.WaitForIdle(ctx, sessionName, tmux.WaitConfig{
			PromptPrefix:    tmpl.PromptPrefix,
			BusyIndicators:  tmpl.BusyIndicators,
			ConsecutiveIdle: 2,
			PollInterval:    200 * time.Millisecond,
		}, timeout)
	}

	// Capture output and exit status before destroying the session.
	output, _ := h.Tmux.CaptureOutput(sessionName, 0)
	exitCode := h.Tmux.PaneExitStatus(sessionName)
	if strings.TrimSpace(output) != "" {
		_ = os.WriteFile(filepath.Join(stageDir, "response.md"), []byte(output), 0o644)
	}

	// Parse agent conversation log and emit RunLog events.
	if exec.Engine != nil && exec.Engine.RunLog != nil && tmpl.LogLocator != nil {
		h.emitAgentLogEvents(exec, node.ID, tmpl, sessionStartTime)
	}

	// Clean up session.
	_ = h.Tmux.DestroySession(sessionName)

	// Emit completion event.
	if exec.Engine != nil {
		exec.Engine.AppendProgress(map[string]any{
			"event":      "tmux_session_complete",
			"node_id":    node.ID,
			"tool":       toolName,
			"session":    sessionName,
			"exit_code":  exitCode,
			"output_len": len(output),
			"wait_error": fmt.Sprint(waitErr),
		})
	}

	if waitErr != nil {
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("agent timeout: %v", waitErr),
			Meta:          map[string]any{"failure_class": "transient_infra"},
			ContextUpdates: map[string]any{
				"failure_class": "transient_infra",
				"last_stage":    node.ID,
				"last_response": engine.Truncate(output, 200),
			},
		}, nil
	}

	// Check exit code for failure detection.
	if exitCode > 0 {
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("agent exited with code %d", exitCode),
			Meta:          map[string]any{"failure_class": "deterministic", "exit_code": exitCode},
			ContextUpdates: map[string]any{
				"failure_class": "deterministic",
				"last_stage":    node.ID,
				"last_response": engine.Truncate(output, 200),
			},
		}, nil
	}

	return runtime.Outcome{
		Status: runtime.StatusSuccess,
		Notes:  fmt.Sprintf("agent completed via tmux (%s)", toolName),
		ContextUpdates: map[string]any{
			"last_stage":    node.ID,
			"last_response": engine.Truncate(output, 200),
		},
	}, nil
}

// emitAgentLogEvents finds and parses the agent's conversation log, emitting events to RunLog.
func (h *TmuxAgentHandler) emitAgentLogEvents(exec *engine.Execution, nodeID string, tmpl *templates.Template, startedAfter time.Time) {
	logPath, err := tmpl.LogLocator.FindLog(exec.WorktreeDir, startedAfter)
	if err != nil {
		exec.Engine.RunLog.Warn("agent", nodeID, "agent.log_not_found", fmt.Sprintf("Agent log not found: %v", err))
		return
	}

	parser := agentlog.ParserForTool(tmpl.Name)
	if parser == nil {
		return
	}

	events, err := parser(logPath)
	if err != nil {
		exec.Engine.RunLog.Warn("agent", nodeID, "agent.log_parse_error", fmt.Sprintf("Parse agent log: %v", err))
		return
	}

	for _, ev := range events {
		exec.Engine.RunLog.Info("agent", nodeID, ev.Type, ev.Message, ev.Data)
	}
}

// handleStartupDialog polls for a startup dialog and dismisses it.
func (h *TmuxAgentHandler) handleStartupDialog(session string, dialog templates.StartupDialog, timeout time.Duration) {
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		lines, _ := h.Tmux.CaptureLines(session, 15)
		content := strings.Join(lines, "\n")
		detected := false
		for _, pattern := range dialog.DetectPatterns {
			if strings.Contains(content, pattern) {
				detected = true
				break
			}
		}
		if detected {
			for _, key := range dialog.Keys {
				h.Tmux.SendKeys(session, key)
				time.Sleep(200 * time.Millisecond)
			}
			if dialog.DelayAfter > 0 {
				time.Sleep(dialog.DelayAfter)
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// resolveToolName determines which CLI tool to use for a node.
func resolveToolName(node *model.Node) string {
	// Check explicit node attribute first.
	if tool := strings.TrimSpace(node.Attr("agent_tool", "")); tool != "" {
		return tool
	}
	// Check llm_provider for provider-based routing.
	if provider := strings.TrimSpace(node.Attr("llm_provider", "")); provider != "" {
		switch strings.ToLower(provider) {
		case "anthropic":
			return "claude"
		case "openai":
			return "codex"
		case "google", "gemini":
			return "gemini"
		}
	}
	return "claude" // default
}

// buildSessionName creates a unique tmux session name for a node execution.
func buildSessionName(runID, nodeID string) string {
	name := "kilroy"
	if runID != "" {
		name += "-" + runID
	}
	name += "-" + nodeID
	// Truncate and sanitize for tmux.
	if len(name) > 128 {
		name = name[:128]
	}
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}
