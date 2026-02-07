package engine

import (
	"context"
	"encoding/json"
	"errors"
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
		text, used, err := r.withFailoverText(ctx, execCtx, node, client, provider, modelID, func(prov string, mid string) (string, error) {
			req := llm.Request{
				Provider:        prov,
				Model:           mid,
				Messages:        []llm.Message{llm.User(prompt)},
				ReasoningEffort: reasoningPtr,
			}
			if err := writeJSON(filepath.Join(stageDir, "api_request.json"), req); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write api_request.json: %v", err))
			}
			policy := attractorLLMRetryPolicy(execCtx, node.ID, prov, mid)
			resp, err := llm.Retry(ctx, policy, nil, nil, func() (llm.Response, error) {
				return client.Complete(ctx, req)
			})
			if err != nil {
				return "", err
			}
			if err := writeJSON(filepath.Join(stageDir, "api_response.json"), resp.Raw); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write api_response.json: %v", err))
			}
			return resp.Text(), nil
		})
		if err != nil {
			return "", nil, err
		}
		_ = writeJSON(filepath.Join(stageDir, "provider_used.json"), map[string]any{
			"backend":  "api",
			"mode":     mode,
			"provider": used.Provider,
			"model":    used.Model,
		})
		return text, nil, nil
	case "agent_loop":
		env := agent.NewLocalExecutionEnvironment(execCtx.WorktreeDir)
		text, used, err := r.withFailoverText(ctx, execCtx, node, client, provider, modelID, func(prov string, mid string) (string, error) {
			profile, err := profileForProvider(prov, mid)
			if err != nil {
				return "", err
			}
			sessCfg := agent.SessionConfig{}
			if reasoning != "" {
				sessCfg.ReasoningEffort = reasoning
			}
			if v := parseInt(node.Attr("max_agent_turns", ""), 0); v > 0 {
				sessCfg.MaxTurns = v
			}
			// Give lots of room for transient LLM errors before failing the stage.
			policy := attractorLLMRetryPolicy(execCtx, node.ID, prov, mid)
			sessCfg.LLMRetryPolicy = &policy
			sess, err := agent.NewSession(client, profile, env, sessCfg)
			if err != nil {
				return "", err
			}

			eventsPath := filepath.Join(stageDir, "events.ndjson")
			eventsJSONPath := filepath.Join(stageDir, "events.json")
			eventsFile, err := os.Create(eventsPath)
			if err != nil {
				return "", err
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
				return text, runErr
			}
			return text, nil
		})
		if err != nil {
			return "", nil, err
		}
		_ = writeJSON(filepath.Join(stageDir, "provider_used.json"), map[string]any{
			"backend":  "api",
			"mode":     mode,
			"provider": used.Provider,
			"model":    used.Model,
		})
		return text, nil, nil
	default:
		return "", nil, fmt.Errorf("invalid codergen_mode: %q (want one_shot|agent_loop)", mode)
	}
}

type providerModel struct {
	Provider string
	Model    string
}

func (r *CodergenRouter) withFailoverText(
	ctx context.Context,
	execCtx *Execution,
	node *model.Node,
	client *llm.Client,
	primaryProvider string,
	primaryModel string,
	attempt func(provider string, model string) (string, error),
) (string, providerModel, error) {
	primaryProvider = normalizeProviderKey(primaryProvider)
	primaryModel = strings.TrimSpace(primaryModel)

	available := map[string]bool{}
	if client != nil {
		for _, p := range client.ProviderNames() {
			available[normalizeProviderKey(p)] = true
		}
	}

	cands := []providerModel{{Provider: primaryProvider, Model: primaryModel}}
	for _, p := range failoverOrder(primaryProvider) {
		p = normalizeProviderKey(p)
		if p == "" || p == primaryProvider {
			continue
		}
		if r.backendForProvider(p) != BackendAPI {
			continue
		}
		if len(available) > 0 && !available[p] {
			continue
		}
		m := pickFailoverModel(p, r.catalog)
		if strings.TrimSpace(m) == "" {
			continue
		}
		cands = append(cands, providerModel{Provider: p, Model: m})
	}

	var lastErr error
	for i, c := range cands {
		if ctx.Err() != nil {
			return "", providerModel{}, ctx.Err()
		}
		if i > 0 {
			if lastErr == nil || !shouldFailoverLLMError(lastErr) {
				break
			}
			prev := cands[i-1]
			msg := fmt.Sprintf("FAILOVER: node=%s provider=%s model=%s -> provider=%s model=%s (reason=%v)", node.ID, prev.Provider, prev.Model, c.Provider, c.Model, lastErr)
			warnEngine(execCtx, msg)
			// Noisy by design: failover is preferable to hard failure, but should be visible.
			_, _ = fmt.Fprintln(os.Stderr, msg)
			if execCtx != nil && execCtx.Engine != nil {
				execCtx.Engine.appendProgress(map[string]any{
					"event":         "llm_failover",
					"node_id":       node.ID,
					"from_provider": prev.Provider,
					"from_model":    prev.Model,
					"to_provider":   c.Provider,
					"to_model":      c.Model,
					"reason":        fmt.Sprint(lastErr),
				})
			}
		}
		txt, err := attempt(c.Provider, c.Model)
		if err == nil {
			return txt, c, nil
		}
		lastErr = err
		if !shouldFailoverLLMError(err) {
			return "", c, err
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("llm call failed (no attempts made)")
	}
	return "", cands[0], lastErr
}

func attractorLLMRetryPolicy(execCtx *Execution, nodeID string, provider string, modelID string) llm.RetryPolicy {
	// DefaultUnifiedLLM retries are conservative; Attractor runs should allow more headroom.
	p := llm.DefaultRetryPolicy()
	p.MaxRetries = 6
	p.BaseDelay = 2 * time.Second
	p.MaxDelay = 120 * time.Second
	p.BackoffMultiplier = 2.0
	p.Jitter = true
	maxRetries := p.MaxRetries
	p.OnRetry = func(err error, attempt int, delay time.Duration) {
		msg := fmt.Sprintf("llm retry (node=%s provider=%s model=%s attempt=%d/%d delay=%s): %v", nodeID, provider, modelID, attempt, maxRetries+1, delay, err)
		warnEngine(execCtx, msg)
		if execCtx != nil && execCtx.Engine != nil {
			execCtx.Engine.appendProgress(map[string]any{
				"event":     "llm_retry",
				"node_id":   nodeID,
				"provider":  provider,
				"model":     modelID,
				"attempt":   attempt,
				"max":       maxRetries + 1,
				"delay_ms":  delay.Milliseconds(),
				"error":     fmt.Sprint(err),
				"retryable": true,
			})
		}
	}
	return p
}

func shouldFailoverLLMError(err error) bool {
	if err == nil {
		return false
	}
	var ce *llm.ConfigurationError
	if errors.As(err, &ce) {
		return false
	}
	var ae *llm.AuthenticationError
	if errors.As(err, &ae) {
		return false
	}
	var ade *llm.AccessDeniedError
	if errors.As(err, &ade) {
		return false
	}
	var ire *llm.InvalidRequestError
	if errors.As(err, &ire) {
		return false
	}
	var cle *llm.ContextLengthError
	if errors.As(err, &cle) {
		return false
	}
	// Timeouts, rate limits, server errors, and unknown transport errors can be
	// provider-specific; failover is often better than hard failure.
	return true
}

func failoverOrder(primary string) []string {
	switch normalizeProviderKey(primary) {
	case "openai":
		return []string{"anthropic", "google"}
	case "anthropic":
		return []string{"openai", "google"}
	case "google":
		return []string{"openai", "anthropic"}
	default:
		return []string{"openai", "anthropic", "google"}
	}
}

func pickFailoverModel(provider string, catalog *modeldb.LiteLLMCatalog) string {
	provider = normalizeProviderKey(provider)
	switch provider {
	case "openai":
		// Prefer the repo's pinned default, even if the catalog doesn't contain it yet.
		if catalog != nil && catalog.Models != nil {
			if _, ok := catalog.Models["gpt-5.2-codex"]; ok {
				return "gpt-5.2-codex"
			}
			if _, ok := catalog.Models["codex-mini-latest"]; ok {
				return "codex-mini-latest"
			}
		}
		return "gpt-5.2-codex"
	case "anthropic":
		best := ""
		for _, id := range modelIDsForProvider(catalog, "anthropic") {
			if best == "" || betterAnthropicModel(id, best) {
				best = id
			}
		}
		return providerModelIDFromCatalogKey("anthropic", best)
	case "google":
		// Prefer a known good "pro" model when present.
		for _, want := range []string{
			"gemini/gemini-2.5-pro",
			"gemini/gemini-2.5-pro-preview-06-05",
			"gemini/gemini-2.5-pro-preview-05-06",
			"gemini/gemini-2.5-pro-preview-03-25",
		} {
			if hasModelID(catalog, "google", want) {
				return providerModelIDFromCatalogKey("google", want)
			}
		}
		best := ""
		for _, id := range modelIDsForProvider(catalog, "google") {
			if best == "" || betterGoogleModel(id, best) {
				best = id
			}
		}
		return providerModelIDFromCatalogKey("google", best)
	default:
		return ""
	}
}

func modelIDsForProvider(catalog *modeldb.LiteLLMCatalog, provider string) []string {
	if catalog == nil || catalog.Models == nil {
		return nil
	}
	provider = normalizeProviderKey(provider)
	out := []string{}
	for id, entry := range catalog.Models {
		if normalizeProviderKey(entry.LiteLLMProvider) != provider {
			continue
		}
		out = append(out, id)
	}
	return out
}

func hasModelID(catalog *modeldb.LiteLLMCatalog, provider string, id string) bool {
	if catalog == nil || catalog.Models == nil {
		return false
	}
	provider = normalizeProviderKey(provider)
	entry, ok := catalog.Models[id]
	if !ok {
		return false
	}
	return normalizeProviderKey(entry.LiteLLMProvider) == provider
}

func providerModelIDFromCatalogKey(provider string, id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	switch normalizeProviderKey(provider) {
	case "google":
		return strings.TrimPrefix(id, "gemini/")
	case "anthropic":
		if i := strings.LastIndex(id, "/"); i >= 0 {
			return id[i+1:]
		}
		return id
	default:
		return id
	}
}

func betterAnthropicModel(a string, b string) bool {
	// Higher rank is better:
	// 1) family: opus > sonnet > haiku
	// 2) numeric tokens (version/date) lexicographically
	// 3) prefer non-region keys
	ra := anthropicFamilyRank(a)
	rb := anthropicFamilyRank(b)
	if ra != rb {
		return ra > rb
	}
	cmp := compareIntSlices(numericTokens(a), numericTokens(b))
	if cmp != 0 {
		return cmp > 0
	}
	pa := strings.Contains(a, "/")
	pb := strings.Contains(b, "/")
	if pa != pb {
		return !pa
	}
	return a > b
}

func anthropicFamilyRank(id string) int {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "opus"):
		return 3
	case strings.Contains(s, "sonnet"):
		return 2
	case strings.Contains(s, "haiku"):
		return 1
	default:
		return 0
	}
}

func betterGoogleModel(a string, b string) bool {
	ra := googleFamilyRank(a)
	rb := googleFamilyRank(b)
	if ra != rb {
		return ra > rb
	}
	cmp := compareIntSlices(numericTokens(a), numericTokens(b))
	if cmp != 0 {
		return cmp > 0
	}
	return a > b
}

func googleFamilyRank(id string) int {
	s := strings.ToLower(id)
	switch {
	case strings.Contains(s, "-pro"):
		return 3
	case strings.Contains(s, "flash"):
		return 2
	case strings.Contains(s, "lite"):
		return 1
	default:
		return 0
	}
}

func numericTokens(s string) []int {
	out := []int{}
	n := 0
	in := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			in = true
			n = n*10 + int(c-'0')
			continue
		}
		if in {
			out = append(out, n)
			n = 0
			in = false
		}
	}
	if in {
		out = append(out, n)
	}
	return out
}

func compareIntSlices(a []int, b []int) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] == b[i] {
			continue
		}
		if a[i] < b[i] {
			return -1
		}
		return 1
	}
	if len(a) == len(b) {
		return 0
	}
	if len(a) < len(b) {
		return -1
	}
	return 1
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
