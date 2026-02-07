package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/strongdm/kilroy/internal/agent"
	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/modeldb"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
	"github.com/strongdm/kilroy/internal/llm"
	"github.com/strongdm/kilroy/internal/llmclient"
)

type CodergenRouter struct {
	cfg     *RunConfigFile
	catalog *modeldb.LiteLLMCatalog

	apiOnce   sync.Once
	apiClient *llm.Client
	apiErr    error
}

func NewCodergenRouter(cfg *RunConfigFile, catalog *modeldb.LiteLLMCatalog) *CodergenRouter {
	return &CodergenRouter{cfg: cfg, catalog: catalog}
}

func (r *CodergenRouter) Run(ctx context.Context, exec *Execution, node *model.Node, prompt string) (string, *runtime.Outcome, error) {
	_ = r.catalog // used later for context window + pricing metadata

	prov := normalizeProviderKey(node.Attr("llm_provider", ""))
	if prov == "" {
		return "", nil, fmt.Errorf("missing llm_provider on node %s", node.ID)
	}
	modelID := strings.TrimSpace(node.Attr("llm_model", ""))
	if modelID == "" {
		// Best-effort compatibility with stylesheet examples that use "model".
		modelID = strings.TrimSpace(node.Attr("model", ""))
	}
	if modelID == "" {
		return "", nil, fmt.Errorf("missing llm_model on node %s", node.ID)
	}
	backend := r.backendForProvider(prov)
	if backend == "" {
		return "", nil, fmt.Errorf("no backend configured for provider %s", prov)
	}

	switch backend {
	case BackendAPI:
		return r.runAPI(ctx, exec, node, prov, modelID, prompt)
	case BackendCLI:
		return r.runCLI(ctx, exec, node, prov, modelID, prompt)
	default:
		return "", nil, fmt.Errorf("invalid backend for provider %s: %q", prov, backend)
	}
}

func (r *CodergenRouter) backendForProvider(provider string) BackendKind {
	if r.cfg == nil {
		return ""
	}
	for k, v := range r.cfg.LLM.Providers {
		if normalizeProviderKey(k) != strings.ToLower(strings.TrimSpace(provider)) {
			continue
		}
		return v.Backend
	}
	return ""
}

func (r *CodergenRouter) api() (*llm.Client, error) {
	r.apiOnce.Do(func() {
		r.apiClient, r.apiErr = llmclient.NewFromEnv()
	})
	return r.apiClient, r.apiErr
}

func (r *CodergenRouter) runAPI(ctx context.Context, execCtx *Execution, node *model.Node, provider string, modelID string, prompt string) (string, *runtime.Outcome, error) {
	client, err := r.api()
	if err != nil {
		return "", nil, err
	}
	mode := strings.ToLower(strings.TrimSpace(node.Attr("codergen_mode", "")))
	if mode == "" {
		mode = "agent_loop" // metaspec default for API backend
	}

	stageDir := filepath.Join(execCtx.LogsRoot, node.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}

	reasoning := strings.TrimSpace(node.Attr("reasoning_effort", ""))
	var reasoningPtr *string
	if reasoning != "" {
		reasoningPtr = &reasoning
	}

	switch mode {
	case "one_shot":
		req := llm.Request{
			Provider:        provider,
			Model:           modelID,
			Messages:        []llm.Message{llm.User(prompt)},
			ReasoningEffort: reasoningPtr,
		}
		if err := writeJSON(filepath.Join(stageDir, "api_request.json"), req); err != nil {
			warnEngine(execCtx, fmt.Sprintf("write api_request.json: %v", err))
		}
		resp, err := client.Complete(ctx, req)
		if err != nil {
			return "", nil, err
		}
		if err := writeJSON(filepath.Join(stageDir, "api_response.json"), resp.Raw); err != nil {
			warnEngine(execCtx, fmt.Sprintf("write api_response.json: %v", err))
		}
		return resp.Text(), nil, nil
	case "agent_loop":
		env := agent.NewLocalExecutionEnvironment(execCtx.WorktreeDir)
		profile, err := profileForProvider(provider, modelID)
		if err != nil {
			return "", nil, err
		}
		sessCfg := agent.SessionConfig{}
		if reasoning != "" {
			sessCfg.ReasoningEffort = reasoning
		}
		if v := parseInt(node.Attr("max_agent_turns", ""), 0); v > 0 {
			sessCfg.MaxTurns = v
		}
		sess, err := agent.NewSession(client, profile, env, sessCfg)
		if err != nil {
			return "", nil, err
		}
		eventsPath := filepath.Join(stageDir, "events.ndjson")
		eventsJSONPath := filepath.Join(stageDir, "events.json")
		eventsFile, err := os.Create(eventsPath)
		if err != nil {
			return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
		}
		defer func() { _ = eventsFile.Close() }()

		var eventsMu sync.Mutex
		var events []agent.SessionEvent
		done := make(chan struct{})
		go func() {
			enc := json.NewEncoder(eventsFile)
			encodeFailed := false
			for ev := range sess.Events() {
				if !encodeFailed {
					if err := enc.Encode(ev); err != nil {
						encodeFailed = true
						warnEngine(execCtx, fmt.Sprintf("write %s: %v", eventsPath, err))
					}
				}
				// Best-effort: emit normalized tool call/result turns to CXDB.
				if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.CXDB != nil {
					emitCXDBToolTurns(ctx, execCtx.Engine, node.ID, ev)
				}
				eventsMu.Lock()
				events = append(events, ev)
				eventsMu.Unlock()
			}
			close(done)
		}()

		text, runErr := sess.ProcessInput(ctx, prompt)
		sess.Close()
		<-done
		eventsMu.Lock()
		if err := writeJSON(eventsJSONPath, events); err != nil {
			warnEngine(execCtx, fmt.Sprintf("write %s: %v", eventsJSONPath, err))
		}
		eventsMu.Unlock()
		if runErr != nil {
			return text, nil, runErr
		}
		return text, nil, nil
	default:
		return "", nil, fmt.Errorf("invalid codergen_mode: %q (want one_shot|agent_loop)", mode)
	}
}

func profileForProvider(provider string, modelID string) (agent.ProviderProfile, error) {
	switch normalizeProviderKey(provider) {
	case "openai":
		return agent.NewOpenAIProfile(modelID), nil
	case "anthropic":
		return agent.NewAnthropicProfile(modelID), nil
	case "google":
		return agent.NewGeminiProfile(modelID), nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
	}
}

func (r *CodergenRouter) runCLI(ctx context.Context, execCtx *Execution, node *model.Node, provider string, modelID string, prompt string) (string, *runtime.Outcome, error) {
	stageDir := filepath.Join(execCtx.LogsRoot, node.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}

	exe, args := defaultCLIInvocation(provider, modelID, execCtx.WorktreeDir)
	if exe == "" {
		return "", nil, fmt.Errorf("no CLI mapping for provider %s", provider)
	}

	// Metaspec: if a provider CLI supports both an event stream and a structured final JSON output,
	// capture both. For Codex this is `--output-schema <schema.json> -o <output.json>`.
	//
	// This is best-effort: if a given CLI build/version doesn't support these flags, the run will
	// fail fast (which is preferred to silently dropping observability artifacts).
	var structuredOutPath string
	var structuredSchemaPath string
	if normalizeProviderKey(provider) == "openai" {
		structuredSchemaPath = filepath.Join(stageDir, "output_schema.json")
		structuredOutPath = filepath.Join(stageDir, "output.json")
		if err := os.WriteFile(structuredSchemaPath, []byte(defaultCodexOutputSchema), 0o644); err != nil {
			return "", nil, err
		}
		if !hasArg(args, "--output-schema") {
			args = append(args, "--output-schema", structuredSchemaPath)
		}
		if !hasArg(args, "-o") && !hasArg(args, "--output") {
			args = append(args, "-o", structuredOutPath)
		}
	}

	actualArgs := args
	recordedArgs := args
	promptMode := "stdin"
	switch normalizeProviderKey(provider) {
	case "anthropic", "google":
		promptMode = "arg"
		actualArgs = insertPromptArg(args, prompt)
		recordedArgs = insertPromptArg(args, "<prompt>")
	}

	inv := map[string]any{
		"provider":     provider,
		"model":        modelID,
		"executable":   exe,
		"argv":         recordedArgs,
		"working_dir":  execCtx.WorktreeDir,
		"prompt_mode":  promptMode,
		"prompt_bytes": len(prompt),
		// Metaspec: capture how env was populated so the invocation is replayable.
		"env_mode":      "inherit",
		"env_allowlist": []string{"*"},
	}
	if structuredOutPath != "" {
		inv["structured_output_path"] = structuredOutPath
	}
	if structuredSchemaPath != "" {
		inv["structured_output_schema_path"] = structuredSchemaPath
	}
	if err := writeJSON(filepath.Join(stageDir, "cli_invocation.json"), inv); err != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}

	cmd := exec.CommandContext(ctx, exe, actualArgs...)
	cmd.Dir = execCtx.WorktreeDir
	if promptMode == "stdin" {
		cmd.Stdin = strings.NewReader(prompt)
	} else {
		// Avoid interactive reads if the CLI tries stdin for confirmations.
		cmd.Stdin = strings.NewReader("")
	}
	stdoutPath := filepath.Join(stageDir, "stdout.log")
	stderrPath := filepath.Join(stageDir, "stderr.log")
	stdoutFile, err := os.Create(stdoutPath)
	if err != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		_ = stdoutFile.Close()
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}
	defer func() { _ = stdoutFile.Close(); _ = stderrFile.Close() }()
	cmd.Stdout = stdoutFile
	cmd.Stderr = stderrFile

	start := time.Now()
	runErr := cmd.Run()
	dur := time.Since(start)
	exitCode := -1
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}

	// Best-effort: treat stdout as ndjson if it parses line-by-line.
	wroteJSON, hadContent, ndErr := bestEffortNDJSON(stageDir, stdoutPath)
	if ndErr != nil {
		return "", &runtime.Outcome{Status: runtime.StatusFail, FailureReason: ndErr.Error()}, nil
	}
	if hadContent && !wroteJSON {
		warnEngine(execCtx, "stdout was not valid ndjson; wrote events.ndjson only")
	}
	if err := writeJSON(filepath.Join(stageDir, "cli_timing.json"), map[string]any{
		"duration_ms": dur.Milliseconds(),
		"exit_code":   exitCode,
	}); err != nil {
		warnEngine(execCtx, fmt.Sprintf("write cli_timing.json: %v", err))
	}

	outStr := ""
	if outBytes, rerr := os.ReadFile(stdoutPath); rerr != nil {
		warnEngine(execCtx, fmt.Sprintf("read stdout.log: %v", rerr))
	} else {
		outStr = string(outBytes)
	}
	if runErr != nil {
		return outStr, &runtime.Outcome{Status: runtime.StatusFail, FailureReason: runErr.Error()}, nil
	}
	return outStr, nil, nil
}

func insertPromptArg(args []string, prompt string) []string {
	if prompt == "" {
		return append([]string{}, args...)
	}
	out := []string{}
	for i := 0; i < len(args); i++ {
		out = append(out, args[i])
		if args[i] == "-p" || args[i] == "--print" || args[i] == "--prompt" {
			out = append(out, prompt)
			// Only insert once.
			out = append(out, args[i+1:]...)
			return out
		}
	}
	out = append(out, prompt)
	return out
}

func emitCXDBToolTurns(ctx context.Context, eng *Engine, nodeID string, ev agent.SessionEvent) {
	if eng == nil || eng.CXDB == nil {
		return
	}
	if ev.Data == nil {
		return
	}
	runID := eng.Options.RunID
	switch ev.Kind {
	case agent.EventToolCallStart:
		toolName := strings.TrimSpace(fmt.Sprint(ev.Data["tool_name"]))
		callID := strings.TrimSpace(fmt.Sprint(ev.Data["call_id"]))
		argsJSON := strings.TrimSpace(fmt.Sprint(ev.Data["arguments_json"]))
		if toolName == "" || callID == "" {
			return
		}
		if _, _, err := eng.CXDB.Append(ctx, "com.kilroy.attractor.ToolCall", 1, map[string]any{
			"run_id":         runID,
			"node_id":        nodeID,
			"tool_name":      toolName,
			"call_id":        callID,
			"arguments_json": argsJSON,
		}); err != nil {
			eng.Warn(fmt.Sprintf("cxdb append ToolCall failed (node=%s tool=%s call_id=%s): %v", nodeID, toolName, callID, err))
		}
	case agent.EventToolCallEnd:
		toolName := strings.TrimSpace(fmt.Sprint(ev.Data["tool_name"]))
		callID := strings.TrimSpace(fmt.Sprint(ev.Data["call_id"]))
		if toolName == "" || callID == "" {
			return
		}
		isErr, _ := ev.Data["is_error"].(bool)
		fullOutput := fmt.Sprint(ev.Data["full_output"])
		if _, _, err := eng.CXDB.Append(ctx, "com.kilroy.attractor.ToolResult", 1, map[string]any{
			"run_id":    runID,
			"node_id":   nodeID,
			"tool_name": toolName,
			"call_id":   callID,
			"output":    truncate(fullOutput, 8_000),
			"is_error":  isErr,
		}); err != nil {
			eng.Warn(fmt.Sprintf("cxdb append ToolResult failed (node=%s tool=%s call_id=%s): %v", nodeID, toolName, callID, err))
		}
	}
}

func defaultCLIInvocation(provider string, modelID string, worktreeDir string) (exe string, args []string) {
	switch normalizeProviderKey(provider) {
	case "openai":
		exe = envOr("KILROY_CODEX_PATH", "codex")
		args = []string{"exec", "--json", "--ask-for-approval", "never", "--sandbox", "workspace-write", "-m", modelID, "-C", worktreeDir}
	case "anthropic":
		exe = envOr("KILROY_CLAUDE_PATH", "claude")
		args = []string{"-p", "--output-format", "stream-json", "--model", modelID}
	case "google":
		exe = envOr("KILROY_GEMINI_PATH", "gemini")
		args = []string{"-p", "--output-format", "stream-json", "--model", modelID}
	default:
		return "", nil
	}
	return exe, args
}

func envOr(key string, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func hasArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

const defaultCodexOutputSchema = `{
  "type": "object",
  "properties": {
    "final": { "type": "string" },
    "summary": { "type": "string" }
  },
  "additionalProperties": true
}
`

// bestEffortNDJSON always writes events.ndjson (a copy of stdout.log) and, if the
// file is valid ndjson, also writes events.json as a JSON array.
//
// Returns wroteJSON=true if events.json was written.
func bestEffortNDJSON(stageDir string, stdoutPath string) (wroteJSON bool, hadContent bool, err error) {
	b, err := os.ReadFile(stdoutPath)
	if err != nil {
		return false, false, err
	}
	if err := os.WriteFile(filepath.Join(stageDir, "events.ndjson"), b, 0o644); err != nil {
		return false, false, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 {
		return false, false, nil
	}
	var objs []any
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}
		hadContent = true
		var v any
		if err := json.Unmarshal([]byte(l), &v); err != nil {
			return false, hadContent, nil
		}
		objs = append(objs, v)
	}
	if len(objs) == 0 {
		return false, hadContent, nil
	}
	if err := writeJSON(filepath.Join(stageDir, "events.json"), objs); err != nil {
		return false, hadContent, err
	}
	return true, hadContent, nil
}

func warnEngine(execCtx *Execution, msg string) {
	if execCtx == nil || execCtx.Engine == nil {
		return
	}
	execCtx.Engine.Warn(msg)
}
