package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
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
	catalog *modeldb.Catalog

	providerRuntimes map[string]ProviderRuntime
	apiClientFactory func(map[string]ProviderRuntime) (*llm.Client, error)

	apiOnce   sync.Once
	apiClient *llm.Client
	apiErr    error
}

func NewCodergenRouter(cfg *RunConfigFile, catalog *modeldb.Catalog) *CodergenRouter {
	return NewCodergenRouterWithRuntimes(cfg, catalog, nil)
}

func NewCodergenRouterWithRuntimes(cfg *RunConfigFile, catalog *modeldb.Catalog, runtimes map[string]ProviderRuntime) *CodergenRouter {
	return &CodergenRouter{
		cfg:              cfg,
		catalog:          catalog,
		providerRuntimes: cloneProviderRuntimeMap(runtimes),
		apiClientFactory: newAPIClientFromProviderRuntimes,
	}
}

func cloneProviderRuntimeMap(in map[string]ProviderRuntime) map[string]ProviderRuntime {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ProviderRuntime, len(in))
	for key, runtime := range in {
		cp := runtime
		cp.Failover = append([]string{}, runtime.Failover...)
		cp.APIHeadersMap = cloneStringMap(runtime.APIHeadersMap)
		cp.CLI = cloneCLISpec(runtime.CLI)
		out[key] = cp
	}
	return out
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
	if exec != nil && exec.Engine != nil {
		if forcedModelID, forced := forceModelForProvider(exec.Engine.Options.ForceModels, prov); forced {
			if !strings.EqualFold(modelID, forcedModelID) {
				warnEngine(exec, fmt.Sprintf("force-model override applied: node=%s provider=%s model=%s (was %s)", node.ID, prov, forcedModelID, modelID))
			}
			modelID = forcedModelID
		}
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
	key := normalizeProviderKey(provider)
	if key == "" {
		return ""
	}
	if rt, ok := r.providerRuntimes[key]; ok {
		return rt.Backend
	}
	if r.cfg == nil {
		return ""
	}
	for k, v := range r.cfg.LLM.Providers {
		if normalizeProviderKey(k) != key {
			continue
		}
		return v.Backend
	}
	return ""
}

func (r *CodergenRouter) ensureAPIClient() (*llm.Client, error) {
	r.apiOnce.Do(func() {
		if len(r.providerRuntimes) > 0 && r.apiClientFactory != nil {
			client, err := r.apiClientFactory(r.providerRuntimes)
			if err != nil {
				r.apiErr = err
				return
			}
			if len(client.ProviderNames()) > 0 {
				r.apiClient = client
				return
			}
		}
		r.apiClient, r.apiErr = llmclient.NewFromEnv()
	})
	return r.apiClient, r.apiErr
}

func (r *CodergenRouter) runAPI(ctx context.Context, execCtx *Execution, node *model.Node, provider string, modelID string, prompt string) (string, *runtime.Outcome, error) {
	client, err := r.ensureAPIClient()
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
			var profile agent.ProviderProfile
			var profileErr error
			if rt, ok := r.providerRuntimes[normalizeProviderKey(prov)]; ok {
				profile, profileErr = profileForRuntimeProvider(rt, mid)
			} else {
				profile, profileErr = profileForProvider(prov, mid)
			}
			if profileErr != nil {
				return "", profileErr
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
	forceModel := func(provider string) (string, bool) {
		if execCtx == nil || execCtx.Engine == nil {
			return "", false
		}
		return forceModelForProvider(execCtx.Engine.Options.ForceModels, provider)
	}
	if forcedModel, forced := forceModel(primaryProvider); forced {
		primaryModel = forcedModel
	}

	available := map[string]bool{}
	if client != nil {
		for _, p := range client.ProviderNames() {
			available[normalizeProviderKey(p)] = true
		}
	}

	cands := []providerModel{{Provider: primaryProvider, Model: primaryModel}}
	order := failoverOrderFromRuntime(primaryProvider, r.providerRuntimes)
	if len(order) == 0 {
		order = failoverOrder(primaryProvider)
	}
	for _, p := range order {
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
		m := ""
		if forcedModel, forced := forceModel(p); forced {
			m = forcedModel
		} else {
			m = pickFailoverModelFromRuntime(p, r.providerRuntimes, r.catalog, primaryModel)
		}
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
	// Keep the current default assignment first.
	p.MaxRetries = 6
	p.BaseDelay = 2 * time.Second
	p.MaxDelay = 120 * time.Second
	p.BackoffMultiplier = 2.0
	p.Jitter = true
	// Override from RunOptions when explicitly configured.
	if execCtx != nil && execCtx.Engine != nil && execCtx.Engine.Options.MaxLLMRetries != nil {
		p.MaxRetries = *execCtx.Engine.Options.MaxLLMRetries
	}
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
	var nfe *llm.NotFoundError
	if errors.As(err, &nfe) {
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

func failoverOrderFromRuntime(primary string, runtimes map[string]ProviderRuntime) []string {
	primary = normalizeProviderKey(primary)
	if primary == "" || len(runtimes) == 0 {
		return nil
	}
	rt, ok := runtimes[primary]
	if !ok || len(rt.Failover) == 0 {
		return nil
	}
	return append([]string{}, rt.Failover...)
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

func pickFailoverModelFromRuntime(provider string, runtimes map[string]ProviderRuntime, catalog *modeldb.Catalog, fallbackModel string) string {
	provider = normalizeProviderKey(provider)
	if provider == "" {
		return strings.TrimSpace(fallbackModel)
	}
	if model := strings.TrimSpace(pickFailoverModel(provider, catalog)); model != "" {
		return model
	}
	ids := modelIDsForProvider(catalog, provider)
	if len(ids) > 0 {
		return providerModelIDFromCatalogKey(provider, ids[0])
	}
	return strings.TrimSpace(fallbackModel)
}

func pickFailoverModel(provider string, catalog *modeldb.Catalog) string {
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

func modelIDsForProvider(catalog *modeldb.Catalog, provider string) []string {
	if catalog == nil || catalog.Models == nil {
		return nil
	}
	provider = normalizeProviderKey(provider)
	out := []string{}
	for id, entry := range catalog.Models {
		if normalizeProviderKey(entry.Provider) != provider {
			continue
		}
		out = append(out, id)
	}
	return out
}

func hasModelID(catalog *modeldb.Catalog, provider string, id string) bool {
	if catalog == nil || catalog.Models == nil {
		return false
	}
	provider = normalizeProviderKey(provider)
	entry, ok := catalog.Models[id]
	if !ok {
		return false
	}
	return normalizeProviderKey(entry.Provider) == provider
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

func profileForRuntimeProvider(rt ProviderRuntime, model string) (agent.ProviderProfile, error) {
	family := strings.TrimSpace(rt.ProfileFamily)
	if family == "" {
		family = rt.Key
	}
	base, err := agent.NewProfileForFamily(family, model)
	if err != nil {
		return nil, err
	}
	providerKey := normalizeProviderKey(rt.Key)
	if providerKey == "" || providerKey == normalizeProviderKey(base.ID()) {
		return base, nil
	}
	// Keep family-specific tool behavior/prompting while routing requests through
	// the runtime provider key (e.g., kimi uses openai-family tooling but kimi API).
	return providerRoutedProfile{
		ProviderProfile: base,
		providerID:      providerKey,
	}, nil
}

type providerRoutedProfile struct {
	agent.ProviderProfile
	providerID string
}

func (p providerRoutedProfile) ID() string {
	return p.providerID
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
	providerKey := normalizeProviderKey(provider)
	stderrPath := filepath.Join(stageDir, "stderr.log")
	readStderr := func() string {
		b, err := os.ReadFile(stderrPath)
		if err != nil {
			return ""
		}
		return string(b)
	}
	classifiedFailure := func(runErr error, stderr string) *runtime.Outcome {
		c := classifyProviderCLIError(providerKey, stderr, runErr)
		return &runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: c.FailureReason,
			Meta: map[string]any{
				"failure_class":     c.FailureClass,
				"failure_signature": c.FailureSignature,
			},
			ContextUpdates: map[string]any{
				"failure_class": c.FailureClass,
			},
		}
	}
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return "", classifiedFailure(err, ""), nil
	}

	defaultExe, args := defaultCLIInvocation(provider, modelID, execCtx.WorktreeDir)
	if defaultExe == "" {
		return "", classifiedFailure(fmt.Errorf("no cli invocation mapping for provider %s", provider), ""), nil
	}
	runOpts := RunOptions{}
	if execCtx != nil && execCtx.Engine != nil {
		runOpts = execCtx.Engine.Options
	}
	exe, err := ResolveProviderExecutable(r.cfg, provider, runOpts)
	if err != nil {
		return "", classifiedFailure(err, ""), nil
	}
	codexSemantics := usesCodexCLISemantics(providerKey, exe)

	var isolatedEnv []string
	var isolatedMeta map[string]any
	if codexSemantics {
		var err error
		isolatedEnv, isolatedMeta, err = buildCodexIsolatedEnv(stageDir)
		if err != nil {
			return "", classifiedFailure(err, ""), nil
		}
	}

	// Metaspec: if a provider CLI supports both an event stream and a structured final JSON output,
	// capture both. For Codex this is `--output-schema <schema.json> -o <output.json>`.
	//
	// This is best-effort: if a given CLI build/version doesn't support these flags, the run will
	// fail fast (which is preferred to silently dropping observability artifacts).
	var structuredOutPath string
	var structuredSchemaPath string
	if codexSemantics {
		structuredSchemaPath = filepath.Join(stageDir, "output_schema.json")
		structuredOutPath = filepath.Join(stageDir, "output.json")
		if err := os.WriteFile(structuredSchemaPath, []byte(defaultCodexOutputSchema), 0o644); err != nil {
			return "", classifiedFailure(err, ""), nil
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
	}
	// Metaspec: capture how env was populated so the invocation is replayable.
	if codexSemantics {
		inv["env_mode"] = "isolated"
		inv["env_scope"] = "codex"
		for k, v := range isolatedMeta {
			inv[k] = v
		}
	} else {
		inv["env_mode"] = "inherit"
		inv["env_allowlist"] = []string{"*"}
		if scrubbed := conflictingProviderEnvKeys(providerKey); len(scrubbed) > 0 {
			inv["env_scrubbed_keys"] = scrubbed
		}
	}
	if structuredOutPath != "" {
		inv["structured_output_path"] = structuredOutPath
	}
	if structuredSchemaPath != "" {
		inv["structured_output_schema_path"] = structuredSchemaPath
	}
	if err := writeJSON(filepath.Join(stageDir, "cli_invocation.json"), inv); err != nil {
		return "", classifiedFailure(err, ""), nil
	}

	stdoutPath := filepath.Join(stageDir, "stdout.log")

	runOnce := func(args []string) (runErr error, exitCode int, dur time.Duration, err error) {
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Dir = execCtx.WorktreeDir
		if codexSemantics {
			cmd.Env = isolatedEnv
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		} else {
			cmd.Env = scrubConflictingProviderEnvKeys(os.Environ(), providerKey)
		}
		if promptMode == "stdin" {
			cmd.Stdin = strings.NewReader(prompt)
		} else {
			// Avoid interactive reads if the CLI tries stdin for confirmations.
			cmd.Stdin = strings.NewReader("")
		}
		stdoutFile, err := os.Create(stdoutPath)
		if err != nil {
			return nil, -1, 0, err
		}
		defer func() { _ = stdoutFile.Close() }()
		stderrFile, err := os.Create(stderrPath)
		if err != nil {
			return nil, -1, 0, err
		}
		defer func() { _ = stderrFile.Close() }()
		cmd.Stdout = stdoutFile
		cmd.Stderr = stderrFile

		start := time.Now()
		if err := cmd.Start(); err != nil {
			return nil, -1, 0, err
		}
		idleTimeout := time.Duration(0)
		killGrace := time.Duration(0)
		if codexSemantics {
			idleTimeout = codexIdleTimeout()
			killGrace = codexKillGrace()
		}
		var idleTimedOut bool
		runErr, idleTimedOut, err = waitWithIdleWatchdog(ctx, cmd, stdoutPath, stderrPath, idleTimeout, killGrace)
		if err != nil {
			return nil, -1, time.Since(start), err
		}
		dur = time.Since(start)
		exitCode = -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		if idleTimedOut {
			inv["failure_trigger"] = "idle_timeout"
			inv["idle_timeout_seconds"] = int(idleTimeout.Seconds())
		}
		return runErr, exitCode, dur, nil
	}

	runArgs := append([]string{}, actualArgs...)
	runErr, exitCode, dur, runErrDetail := runOnce(runArgs)
	if runErrDetail != nil {
		return "", classifiedFailure(runErrDetail, readStderr()), nil
	}

	if runErr != nil && codexSemantics && hasArg(runArgs, "--output-schema") {
		stderrBytes, readErr := os.ReadFile(stderrPath)
		if readErr == nil && isSchemaValidationFailure(string(stderrBytes)) {
			warnEngine(execCtx, "codex schema validation failed; retrying once without --output-schema")
			_ = copyFileContents(stdoutPath, filepath.Join(stageDir, "stdout.schema_failure.log"))
			_ = copyFileContents(stderrPath, filepath.Join(stageDir, "stderr.schema_failure.log"))

			retryArgs := removeArgWithValue(runArgs, "--output-schema")
			inv["schema_fallback_retry"] = true
			inv["schema_fallback_reason"] = "schema_validation_failure"
			inv["argv_schema_retry"] = retryArgs
			if err := writeJSON(filepath.Join(stageDir, "cli_invocation.json"), inv); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write cli_invocation.json fallback metadata: %v", err))
			}

			retryErr, retryExitCode, retryDur, retryRunErr := runOnce(retryArgs)
			if retryRunErr != nil {
				return "", classifiedFailure(retryRunErr, readStderr()), nil
			}
			runErr = retryErr
			exitCode = retryExitCode
			dur += retryDur
			runArgs = retryArgs
		}
	}

	if runErr == nil && codexSemantics && hasArg(runArgs, "--output-schema") && strings.TrimSpace(structuredOutPath) != "" {
		unknownKeys, payload, contractErr := inspectCodexStructuredOutputContract(structuredOutPath)
		if contractErr != nil {
			return "", classifiedFailure(contractErr, readStderr()), nil
		}
		if len(unknownKeys) > 0 {
			warnEngine(execCtx, fmt.Sprintf("codex structured output has unknown keys; retrying once without --output-schema (keys=%s)", strings.Join(unknownKeys, ",")))
			artifact := map[string]any{
				"unknown_keys": unknownKeys,
				"payload":      payload,
			}
			if err := writeJSON(filepath.Join(stageDir, "structured_output_unknown_keys.json"), artifact); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write structured_output_unknown_keys.json: %v", err))
			}

			retryArgs := removeArgWithValue(runArgs, "--output-schema")
			inv["schema_fallback_retry"] = true
			inv["schema_fallback_reason"] = "unknown_structured_keys"
			inv["structured_output_unknown_keys"] = unknownKeys
			inv["argv_schema_retry"] = retryArgs
			if err := writeJSON(filepath.Join(stageDir, "cli_invocation.json"), inv); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write cli_invocation.json unknown-keys metadata: %v", err))
			}

			retryErr, retryExitCode, retryDur, retryRunErr := runOnce(retryArgs)
			if retryRunErr != nil {
				return "", classifiedFailure(retryRunErr, readStderr()), nil
			}
			runErr = retryErr
			exitCode = retryExitCode
			dur += retryDur
			runArgs = retryArgs
		}
	}

	if runErr != nil && codexSemantics {
		stderrBytes, readErr := os.ReadFile(stderrPath)
		if readErr == nil && isStateDBDiscrepancy(string(stderrBytes)) {
			warnEngine(execCtx, "codex state-db discrepancy detected; retrying once with fresh state root")
			_ = copyFileContents(stdoutPath, filepath.Join(stageDir, "stdout.state_db_failure.log"))
			_ = copyFileContents(stderrPath, filepath.Join(stageDir, "stderr.state_db_failure.log"))

			retryEnv, retryMeta, buildErr := buildCodexIsolatedEnvWithName(stageDir, "codex-home-retry1")
			if buildErr != nil {
				return "", classifiedFailure(buildErr, readStderr()), nil
			}
			isolatedEnv = retryEnv
			inv["state_db_fallback_retry"] = true
			inv["state_db_fallback_reason"] = "state_db_record_discrepancy"
			if retryRoot, ok := retryMeta["state_root"]; ok {
				inv["state_db_retry_state_root"] = retryRoot
			}
			if err := writeJSON(filepath.Join(stageDir, "cli_invocation.json"), inv); err != nil {
				warnEngine(execCtx, fmt.Sprintf("write cli_invocation.json state-db metadata: %v", err))
			}

			retryErr, retryExitCode, retryDur, retryRunErr := runOnce(runArgs)
			if retryRunErr != nil {
				return "", classifiedFailure(retryRunErr, readStderr()), nil
			}
			runErr = retryErr
			exitCode = retryExitCode
			dur += retryDur
		}
	}

	// Best-effort: treat stdout as ndjson if it parses line-by-line.
	wroteJSON, hadContent, ndErr := bestEffortNDJSON(stageDir, stdoutPath)
	if ndErr != nil {
		return "", classifiedFailure(ndErr, readStderr()), nil
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
		return outStr, classifiedFailure(runErr, readStderr()), nil
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

func buildCodexIsolatedEnv(stageDir string) ([]string, map[string]any, error) {
	return buildCodexIsolatedEnvWithName(stageDir, "codex-home")
}

func buildCodexIsolatedEnvWithName(stageDir string, homeDirName string) ([]string, map[string]any, error) {
	codexHome, err := codexIsolatedHomeDir(stageDir, homeDirName)
	if err != nil {
		return nil, nil, err
	}
	codexStateRoot := filepath.Join(codexHome, ".codex")
	xdgConfigHome := filepath.Join(codexHome, ".config")
	xdgDataHome := filepath.Join(codexHome, ".local", "share")
	xdgStateHome := filepath.Join(codexHome, ".local", "state")

	for _, dir := range []string{codexHome, codexStateRoot, xdgConfigHome, xdgDataHome, xdgStateHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, nil, err
		}
	}

	seeded := []string{}
	seedErrors := []string{}
	srcHome := strings.TrimSpace(os.Getenv("HOME"))
	if srcHome != "" {
		for _, name := range []string{"auth.json", "config.toml"} {
			src := filepath.Join(srcHome, ".codex", name)
			dst := filepath.Join(codexStateRoot, name)
			copied, err := copyIfExists(src, dst)
			if err != nil {
				seedErrors = append(seedErrors, fmt.Sprintf("%s: %v", name, err))
				continue
			}
			if copied {
				seeded = append(seeded, dst)
			}
		}
	}

	env := mergeEnvWithOverrides(os.Environ(), map[string]string{
		"HOME":            codexHome,
		"CODEX_HOME":      codexStateRoot,
		"XDG_CONFIG_HOME": xdgConfigHome,
		"XDG_DATA_HOME":   xdgDataHome,
		"XDG_STATE_HOME":  xdgStateHome,
	})

	meta := map[string]any{
		"state_base_root":  codexStateBaseRoot(),
		"state_root":       codexStateRoot,
		"env_seeded_files": seeded,
	}
	if len(seedErrors) > 0 {
		meta["env_seed_errors"] = seedErrors
	}
	return env, meta, nil
}

func codexIsolatedHomeDir(stageDir string, homeDirName string) (string, error) {
	absStageDir, err := filepath.Abs(stageDir)
	if err != nil {
		return "", err
	}
	homeDirName = strings.TrimSpace(homeDirName)
	if homeDirName == "" {
		homeDirName = "codex-home"
	}
	sum := sha256.Sum256([]byte(absStageDir + "|" + homeDirName))
	short := hex.EncodeToString(sum[:8])
	return filepath.Join(codexStateBaseRoot(), fmt.Sprintf("%s-%s", homeDirName, short)), nil
}

func codexStateBaseRoot() string {
	if override := strings.TrimSpace(os.Getenv("KILROY_CODEX_STATE_BASE")); override != "" {
		if abs, err := filepath.Abs(override); err == nil {
			return abs
		}
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home := strings.TrimSpace(os.Getenv("HOME"))
		if home == "" {
			base = "."
		} else {
			base = filepath.Join(home, ".local", "state")
		}
	}
	root := filepath.Join(base, "kilroy", "attractor", "codex-state")
	if abs, err := filepath.Abs(root); err == nil {
		return abs
	}
	return root
}

func copyIfExists(src string, dst string) (bool, error) {
	info, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if info.IsDir() {
		return false, fmt.Errorf("source is directory: %s", src)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return false, err
	}
	if err := copyFileContentsWithMode(src, dst, 0o600); err != nil {
		return false, err
	}
	return true, nil
}

func mergeEnvWithOverrides(base []string, overrides map[string]string) []string {
	out := make([]string, 0, len(base)+len(overrides))
	used := map[string]bool{}
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if v, ok := overrides[key]; ok {
			out = append(out, key+"="+v)
			used[key] = true
			continue
		}
		out = append(out, entry)
	}
	remaining := make([]string, 0, len(overrides))
	for k := range overrides {
		if used[k] {
			continue
		}
		remaining = append(remaining, k)
	}
	sort.Strings(remaining)
	for _, k := range remaining {
		out = append(out, k+"="+overrides[k])
	}
	return out
}

// conflictingProviderEnvKeys returns env var names that must be stripped when
// launching a given provider's CLI. The Claude CLI uses OAuth/session-based auth
// by default; an inherited ANTHROPIC_API_KEY causes it to attempt (and fail)
// external API key authentication instead.
func conflictingProviderEnvKeys(providerKey string) []string {
	switch normalizeProviderKey(providerKey) {
	case "anthropic":
		return []string{"ANTHROPIC_API_KEY"}
	default:
		return nil
	}
}

// scrubConflictingProviderEnvKeys returns a copy of the environment with
// provider-conflicting keys removed.
func scrubConflictingProviderEnvKeys(base []string, providerKey string) []string {
	drop := conflictingProviderEnvKeys(providerKey)
	if len(drop) == 0 {
		return base
	}
	dropSet := map[string]bool{}
	for _, k := range drop {
		dropSet[k] = true
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if dropSet[key] {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func codexIdleTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("KILROY_CODEX_IDLE_TIMEOUT"))
	if v == "" {
		return 2 * time.Minute
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 2 * time.Minute
	}
	return d
}

func codexKillGrace() time.Duration {
	v := strings.TrimSpace(os.Getenv("KILROY_CODEX_KILL_GRACE"))
	if v == "" {
		return 2 * time.Second
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 2 * time.Second
	}
	return d
}

func waitWithIdleWatchdog(ctx context.Context, cmd *exec.Cmd, stdoutPath, stderrPath string, idleTimeout, killGrace time.Duration) (runErr error, timedOut bool, err error) {
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	// If the context has a deadline closer than the idle timeout, disable idle
	// timeout and let the context deadline handle termination. This prevents
	// the idle watchdog from killing a process that still has node-level time
	// remaining (e.g., codex thinking for 2+ minutes during a 30-minute node).
	if idleTimeout > 0 {
		if deadline, ok := ctx.Deadline(); ok {
			if remaining := time.Until(deadline); remaining <= idleTimeout+killGrace {
				idleTimeout = 0
			}
		}
	}

	if idleTimeout <= 0 {
		runErr := <-waitCh
		return runErr, false, nil
	}

	const pollInterval = 250 * time.Millisecond
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	ownsProcessGroup := cmd != nil && cmd.SysProcAttr != nil && cmd.SysProcAttr.Setpgid

	lastActivity := time.Now()
	lastStdoutSize, _ := fileSize(stdoutPath)
	lastStderrSize, _ := fileSize(stderrPath)
	for {
		select {
		case waitErr := <-waitCh:
			return waitErr, false, nil
		case <-ticker.C:
			stdoutSize, _ := fileSize(stdoutPath)
			stderrSize, _ := fileSize(stderrPath)
			if stdoutSize != lastStdoutSize || stderrSize != lastStderrSize {
				lastActivity = time.Now()
				lastStdoutSize = stdoutSize
				lastStderrSize = stderrSize
			}
			if time.Since(lastActivity) < idleTimeout {
				continue
			}
			timeoutErr := fmt.Errorf("codex idle timeout after %s with no output", idleTimeout)
			if ownsProcessGroup {
				if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
					return timeoutErr, true, err
				}
			}
			if killGrace > 0 {
				select {
				case <-waitCh:
					return timeoutErr, true, nil
				case <-time.After(killGrace):
				}
			}
			if ownsProcessGroup {
				if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
					return timeoutErr, true, err
				}
			}
			select {
			case <-waitCh:
				return timeoutErr, true, nil
			case <-time.After(2 * time.Second):
				return timeoutErr, true, fmt.Errorf("timed out waiting for process exit after SIGKILL")
			}
		case <-ctx.Done():
			if ownsProcessGroup {
				if err := killProcessGroup(cmd, syscall.SIGTERM); err != nil {
					return ctx.Err(), false, err
				}
				if killGrace > 0 {
					select {
					case <-waitCh:
						return ctx.Err(), false, nil
					case <-time.After(killGrace):
					}
				}
				if err := killProcessGroup(cmd, syscall.SIGKILL); err != nil {
					return ctx.Err(), false, err
				}
				select {
				case <-waitCh:
					return ctx.Err(), false, nil
				case <-time.After(2 * time.Second):
					return ctx.Err(), false, fmt.Errorf("timed out waiting for process exit after context cancellation")
				}
			}
			waitErr := <-waitCh
			if waitErr == nil {
				waitErr = ctx.Err()
			}
			return waitErr, false, nil
		}
	}
}

func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return info.Size(), nil
}

func killProcessGroup(cmd *exec.Cmd, sig syscall.Signal) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	if err := syscall.Kill(-pgid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
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

func usesCodexCLISemantics(providerKey string, exe string) bool {
	if normalizeProviderKey(providerKey) == "openai" {
		return true
	}
	base := strings.ToLower(strings.TrimSpace(filepath.Base(exe)))
	return base == "codex" || strings.HasPrefix(base, "codex.")
}

func defaultCLIInvocation(provider string, modelID string, worktreeDir string) (exe string, args []string) {
	spec := defaultCLISpecForProvider(provider)
	if spec == nil {
		return "", nil
	}
	exe, args = materializeCLIInvocation(*spec, modelID, worktreeDir, "")
	return exe, args
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
  "required": ["final", "summary"],
  "additionalProperties": true
}
`

func isSchemaValidationFailure(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "invalid_json_schema") ||
		strings.Contains(s, "invalid schema for response_format") ||
		strings.Contains(s, "invalid schema")
}

func inspectCodexStructuredOutputContract(outputPath string) ([]string, map[string]any, error) {
	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return nil, nil, err
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, fmt.Errorf("parse structured output %s: %w", outputPath, err)
	}
	requiredKeys := []string{"final", "summary"}
	for _, key := range requiredKeys {
		val, ok := payload[key]
		if !ok {
			return nil, payload, fmt.Errorf("structured output missing required key %q", key)
		}
		if _, ok := val.(string); !ok {
			return nil, payload, fmt.Errorf("structured output key %q must be string", key)
		}
	}
	unknown := make([]string, 0)
	for key := range payload {
		if key == "final" || key == "summary" {
			continue
		}
		unknown = append(unknown, key)
	}
	sort.Strings(unknown)
	return unknown, payload, nil
}

func isStateDBDiscrepancy(stderr string) bool {
	s := strings.ToLower(stderr)
	return strings.Contains(s, "state db missing rollout path") ||
		strings.Contains(s, "state db record_discrepancy") ||
		strings.Contains(s, "record_discrepancy")
}

func removeArgWithValue(args []string, key string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == key {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func copyFileContents(src string, dst string) error {
	return copyFileContentsWithMode(src, dst, 0o644)
}

func copyFileContentsWithMode(src string, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Chmod(perm); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

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
