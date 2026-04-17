package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/agent"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
	"github.com/danshapiro/kilroy/internal/llm"
	"github.com/danshapiro/kilroy/internal/providerspec"
)

type okAdapter struct{ name string }

func (a *okAdapter) Name() string { return a.name }
func (a *okAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}, nil
}
func (a *okAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	return nil, fmt.Errorf("stream not implemented")
}

type scriptedStreamAdapter struct {
	name   string
	script func(s *llm.ChanStream)
}

func (a *scriptedStreamAdapter) Name() string { return a.name }
func (a *scriptedStreamAdapter) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	_ = ctx
	return llm.Response{Provider: a.name, Model: req.Model, Message: llm.Assistant("ok")}, nil
}
func (a *scriptedStreamAdapter) Stream(ctx context.Context, req llm.Request) (llm.Stream, error) {
	_ = ctx
	_ = req
	st := llm.NewChanStream(nil)
	go func() {
		defer st.CloseSend()
		if a.script != nil {
			a.script(st)
		}
	}()
	return st, nil
}

func TestAgentRouter_RunAPI_OneShot_StreamErrorEventTakesPrecedence(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	r := NewAgentRouterWithRuntimes(cfg, nil, map[string]ProviderRuntime{
		"openai": {Key: "openai", Backend: BackendAPI},
	})
	r.apiClientFactory = func(map[string]ProviderRuntime) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(&scriptedStreamAdapter{
			name: "openai",
			script: func(s *llm.ChanStream) {
				s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart})
				s.Send(llm.StreamEvent{Type: llm.StreamEventError, Err: llm.NewStreamError("openai", "synthetic stream failure")})
				resp := llm.Response{Provider: "openai", Model: "gpt-5.2", Message: llm.Assistant("finish should not win")}
				finish := llm.FinishReason{Reason: "stop"}
				usage := llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &finish, Usage: &usage, Response: &resp})
			},
		})
		return client, nil
	}

	execCtx := &Execution{
		LogsRoot:    t.TempDir(),
		WorktreeDir: t.TempDir(),
	}
	node := &model.Node{
		ID:    "stage-a",
		Attrs: map[string]string{"agent_mode": "one_shot"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	text, out, err := r.runAPI(ctx, execCtx, node, "openai", "gpt-5.2", "say hi")
	if err == nil || !strings.Contains(err.Error(), "synthetic stream failure") {
		t.Fatalf("expected stream failure, got err=%v", err)
	}
	if strings.TrimSpace(text) != "" {
		t.Fatalf("text: got %q want empty on stream error", text)
	}
	if out != nil {
		t.Fatalf("outcome: got %+v want nil", out)
	}
}

func TestAgentRouter_RunAPI_OneShot_EmitsProviderToolLifecycleProgress(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	r := NewAgentRouterWithRuntimes(cfg, nil, map[string]ProviderRuntime{
		"openai": {Key: "openai", Backend: BackendAPI},
	})
	r.apiClientFactory = func(map[string]ProviderRuntime) (*llm.Client, error) {
		client := llm.NewClient()
		client.Register(&scriptedStreamAdapter{
			name: "openai",
			script: func(s *llm.ChanStream) {
				s.Send(llm.StreamEvent{Type: llm.StreamEventStreamStart, ID: "turn_1", Model: "gpt-5.2"})
				s.Send(llm.StreamEvent{
					Type:      llm.StreamEventProviderEvent,
					EventType: "item/started",
					Raw: map[string]any{
						"item": map[string]any{
							"id":      "cmd_1",
							"type":    "commandExecution",
							"command": "pwd",
							"cwd":     "/tmp/worktree",
							"status":  "inProgress",
						},
					},
				})
				s.Send(llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: "text_0", Delta: "ok"})
				s.Send(llm.StreamEvent{
					Type:      llm.StreamEventProviderEvent,
					EventType: "item/completed",
					Raw: map[string]any{
						"item": map[string]any{
							"id":     "cmd_1",
							"type":   "commandExecution",
							"status": "completed",
						},
					},
				})
				resp := llm.Response{
					Provider: "openai",
					Model:    "gpt-5.2",
					Message:  llm.Assistant("ok"),
					Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
				}
				s.Send(llm.StreamEvent{Type: llm.StreamEventFinish, FinishReason: &resp.Finish, Response: &resp})
			},
		})
		return client, nil
	}

	var mu sync.Mutex
	captured := make([]map[string]any, 0, 8)
	eng := &Engine{
		Options: RunOptions{RunID: "run-provider-progress"},
		progressSink: func(ev map[string]any) {
			mu.Lock()
			defer mu.Unlock()
			captured = append(captured, ev)
		},
	}
	execCtx := &Execution{
		LogsRoot:    t.TempDir(),
		WorktreeDir: t.TempDir(),
		Engine:      eng,
	}
	node := &model.Node{
		ID:    "stage-a",
		Attrs: map[string]string{"agent_mode": "one_shot"},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	text, out, err := r.runAPI(ctx, execCtx, node, "openai", "gpt-5.2", "say hi")
	if err != nil {
		t.Fatalf("runAPI: %v", err)
	}
	if out != nil {
		t.Fatalf("outcome: got %+v want nil", out)
	}
	if strings.TrimSpace(text) != "ok" {
		t.Fatalf("text: got %q want %q", text, "ok")
	}

	mu.Lock()
	defer mu.Unlock()
	seenStart := false
	seenEnd := false
	seenTurnEndWithCount := false
	for _, ev := range captured {
		eventName, _ := ev["event"].(string)
		switch eventName {
		case "llm_tool_call_start":
			if ev["tool_name"] == "exec_command" && ev["call_id"] == "cmd_1" {
				seenStart = true
			}
		case "llm_tool_call_end":
			if ev["tool_name"] == "exec_command" && ev["call_id"] == "cmd_1" {
				if isErr, ok := ev["is_error"].(bool); !ok || isErr {
					t.Fatalf("expected successful tool completion, got is_error=%v", ev["is_error"])
				}
				seenEnd = true
			}
		case "llm_turn_end":
			if fmt.Sprint(ev["tool_call_count"]) == "1" {
				seenTurnEndWithCount = true
			}
		}
	}
	if !seenStart || !seenEnd {
		t.Fatalf("missing provider tool lifecycle progress events: start=%t end=%t captured=%v", seenStart, seenEnd, captured)
	}
	if !seenTurnEndWithCount {
		t.Fatalf("missing llm_turn_end with tool_call_count=1; captured=%v", captured)
	}
}

func TestAgentRouter_WithFailoverText_FailsOverToDifferentProvider(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai":      {Backend: BackendAPI, Failover: []string{"google"}},
		"google":      {Backend: BackendAPI},
		"unsupported": {Backend: BackendAPI},
	}
	// Only builtin providers are recognized by normalizeProviderKey; others are ignored by withFailoverText.

	catalog := &modeldb.Catalog{
		Models: map[string]modeldb.ModelEntry{
			// Include a provider-prefixed model key to validate providerModelIDFromCatalogKey stripping.
			"gemini/gemini-3.1-pro-preview": {Provider: "google", Mode: "chat"},
		},
	}

	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	r := NewAgentRouterWithRuntimes(cfg, catalog, runtimes)

	client := llm.NewClient()
	client.Register(&okAdapter{name: "openai"})
	client.Register(&okAdapter{name: "google"})

	node := &model.Node{ID: "stage-a"}

	// Capture noisy failover output for determinism.
	oldStderr := os.Stderr
	pr, pw, _ := os.Pipe()
	os.Stderr = pw
	defer func() { os.Stderr = oldStderr }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	txt, used, err := r.withFailoverText(ctx, nil, node, client, "openai", "gpt-5.4", func(prov string, mid string) (string, error) {
		if prov == "openai" {
			return "", fmt.Errorf("synthetic openai failure")
		}
		if prov == "google" {
			if mid != "gemini-3.1-pro-preview" {
				return "", fmt.Errorf("unexpected fallback model: %q", mid)
			}
			return "ok-from-google", nil
		}
		return "", fmt.Errorf("unexpected provider: %q", prov)
	})

	_ = pw.Close()
	_, _ = io.ReadAll(pr)

	if err != nil {
		t.Fatalf("withFailoverText error: %v", err)
	}
	if txt != "ok-from-google" {
		t.Fatalf("text: got %q", txt)
	}
	if used.Provider != "google" {
		t.Fatalf("used provider: got %q want %q", used.Provider, "google")
	}
	if used.Model != "gemini-3.1-pro-preview" {
		t.Fatalf("used model: got %q want %q", used.Model, "gemini-3.1-pro-preview")
	}
}

func TestAgentRouter_WithFailoverText_AppliesForceModelToFailoverProvider(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{"google"}},
		"google": {Backend: BackendAPI},
	}
	catalog := &modeldb.Catalog{
		Models: map[string]modeldb.ModelEntry{
			"gemini/gemini-3.1-pro-preview": {Provider: "google", Mode: "chat"},
		},
	}

	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	r := NewAgentRouterWithRuntimes(cfg, catalog, runtimes)
	client := llm.NewClient()
	client.Register(&okAdapter{name: "openai"})
	client.Register(&okAdapter{name: "google"})

	node := &model.Node{ID: "stage-a"}
	execCtx := &Execution{
		Engine: &Engine{
			Options: RunOptions{
				ForceModels: map[string]string{"google": "gemini-force-override"},
			},
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	txt, used, err := r.withFailoverText(ctx, execCtx, node, client, "openai", "gpt-5.4", func(prov string, mid string) (string, error) {
		if prov == "openai" {
			return "", fmt.Errorf("synthetic openai failure")
		}
		if prov == "google" {
			if mid != "gemini-force-override" {
				return "", fmt.Errorf("unexpected fallback model: %q", mid)
			}
			return "ok-from-google-force", nil
		}
		return "", fmt.Errorf("unexpected provider: %q", prov)
	})
	if err != nil {
		t.Fatalf("withFailoverText error: %v", err)
	}
	if txt != "ok-from-google-force" {
		t.Fatalf("text: got %q", txt)
	}
	if used.Provider != "google" {
		t.Fatalf("used provider: got %q want %q", used.Provider, "google")
	}
	if used.Model != "gemini-force-override" {
		t.Fatalf("used model: got %q want %q", used.Model, "gemini-force-override")
	}
}

func TestProfileForRuntimeProvider_RoutesByRuntimeProviderAndKeepsFamilyBehavior(t *testing.T) {
	rt := ProviderRuntime{Key: "zai", ProfileFamily: "openai"}
	p, err := profileForRuntimeProvider(rt, "glm-4.7")
	if err != nil {
		t.Fatalf("profileForRuntimeProvider: %v", err)
	}
	if p.ID() != "zai" {
		t.Fatalf("expected request routing provider zai, got %q", p.ID())
	}
	sys := p.BuildSystemPrompt(agent.EnvironmentInfo{}, nil)
	if !strings.Contains(sys, "OpenAI profile") {
		t.Fatalf("expected openai-family prompt behavior, got: %q", sys)
	}
}

func TestFailoverOrder_UsesRuntimeProviderPolicy(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"kimi": {Key: "kimi", Failover: []string{"zai", "openai"}, FailoverExplicit: true},
	}
	got, explicit := failoverOrderFromRuntime("kimi", rt)
	if strings.Join(got, ",") != "zai,openai" {
		t.Fatalf("failover mismatch: %v", got)
	}
	if !explicit {
		t.Fatalf("expected explicit failover policy")
	}
}

func TestFailoverOrder_ExplicitEmptyFailoverPreserved(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"openai": {Key: "openai", Failover: []string{}, FailoverExplicit: true},
	}
	got, explicit := failoverOrderFromRuntime("openai", rt)
	if !explicit {
		t.Fatalf("expected explicit=true for empty failover override")
	}
	if len(got) != 0 {
		t.Fatalf("expected empty failover order, got %v", got)
	}
}

func TestAgentRouter_WithFailoverText_ExplicitEmptyFailoverDoesNotFallback(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {
			Backend:  BackendAPI,
			Failover: []string{},
		},
		"anthropic": {
			Backend: BackendAPI,
		},
	}
	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	r := NewAgentRouterWithRuntimes(cfg, nil, runtimes)

	client := llm.NewClient()
	client.Register(&okAdapter{name: "openai"})
	client.Register(&okAdapter{name: "anthropic"})

	attemptedAnthropic := false
	_, _, err = r.withFailoverText(context.Background(), nil, &model.Node{ID: "n1"}, client, "openai", "gpt-5.4", func(prov string, mid string) (string, error) {
		_ = mid
		if prov == "anthropic" {
			attemptedAnthropic = true
		}
		return "", llm.NewNetworkError(prov, "connection reset")
	})
	if err == nil || !strings.Contains(err.Error(), "no failover allowed by runtime config") {
		t.Fatalf("expected explicit no-failover error, got %v", err)
	}
	if attemptedAnthropic {
		t.Fatalf("unexpected failover attempt when failover=[] is explicit")
	}
}

func TestAgentRouter_WithFailoverText_OmittedFailoverDoesNotFallback(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
		"google": {Backend: BackendAPI},
	}
	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	r := NewAgentRouterWithRuntimes(cfg, nil, runtimes)

	attempted := []string{}
	_, used, err := r.withFailoverText(context.Background(), nil, &model.Node{ID: "n1"}, llm.NewClient(), "openai", "gpt-5.4", func(provider, model string) (string, error) {
		_ = model
		attempted = append(attempted, provider)
		return "", llm.NewNetworkError(provider, "synthetic outage")
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if used.Provider != "openai" {
		t.Fatalf("unexpected fallback provider: %q", used.Provider)
	}
	if len(attempted) != 1 || attempted[0] != "openai" {
		t.Fatalf("unexpected fallback attempts: %v", attempted)
	}
}

func TestPickFailoverModelFromRuntime_NeverReturnsEmptyForConfiguredProvider(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"zai": {Key: "zai"},
	}
	got := pickFailoverModelFromRuntime("zai", rt, nil, "glm-4.7")
	if got != "glm-4.7" {
		t.Fatalf("expected fallback model, got %q", got)
	}
}

func TestPickFailoverModelFromRuntime_ZAIDoesNotRotateCatalogVariants(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"zai": {Key: "zai"},
	}
	catalog := &modeldb.Catalog{
		Models: map[string]modeldb.ModelEntry{
			"z-ai/glm-4.6:exacto":   {Provider: "zai"},
			"z-ai/glm-4.5-air:free": {Provider: "zai"},
			"z-ai/glm-4.5v":         {Provider: "zai"},
		},
	}
	got := pickFailoverModelFromRuntime("zai", rt, catalog, "gpt-5.4")
	if got != "glm-4.7" {
		t.Fatalf("expected stable zai model glm-4.7, got %q", got)
	}
}

func TestPickFailoverModelFromRuntime_ZAINormalizesProviderPrefixedFallback(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"zai": {Key: "zai"},
	}
	got := pickFailoverModelFromRuntime("zai", rt, nil, "z-ai/glm-4.7")
	if got != "glm-4.7" {
		t.Fatalf("expected provider-relative zai model, got %q", got)
	}
}

func TestPickFailoverModelFromRuntime_KimiPinnedToK2_5(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"kimi": {Key: "kimi"},
	}
	got := pickFailoverModelFromRuntime("kimi", rt, nil, "gpt-5.4")
	if got != "kimi-k2.5" {
		t.Fatalf("expected stable kimi model kimi-k2.5, got %q", got)
	}
}

func TestPickFailoverModelFromRuntime_CerebrasPinnedToZAIGLM47(t *testing.T) {
	rt := map[string]ProviderRuntime{
		"cerebras": {Key: "cerebras"},
	}
	got := pickFailoverModelFromRuntime("cerebras", rt, nil, "glm-4.7")
	if got != "zai-glm-4.7" {
		t.Fatalf("expected stable cerebras model zai-glm-4.7, got %q", got)
	}
}

func TestEnsureAPIClient_UsesSyncOnce(t *testing.T) {
	var calls atomic.Int32
	r := NewAgentRouterWithRuntimes(&RunConfigFile{}, nil, map[string]ProviderRuntime{
		"openai": {
			Key:     "openai",
			Backend: BackendAPI,
			API: providerspec.APISpec{
				Protocol: providerspec.ProtocolOpenAIResponses,
			},
		},
	})
	r.apiClientFactory = func(map[string]ProviderRuntime) (*llm.Client, error) {
		calls.Add(1)
		c := llm.NewClient()
		c.Register(&okAdapter{name: "openai"})
		return c, nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.ensureAPIClient()
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("api client factory called %d times; want 1", calls.Load())
	}
}

func TestShouldFailoverLLMError_NotFoundDoesNotFailover(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("openai", 404, "model not found", nil, nil)
	if shouldFailoverLLMError(err) {
		t.Fatalf("404 NotFoundError should not trigger failover")
	}
}

func TestShouldFailoverLLMError_ContentFilterDoesNotFailover(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("openai", 400, "blocked by content filter policy", nil, nil)
	if shouldFailoverLLMError(err) {
		t.Fatalf("content filter failures should not trigger failover")
	}
}

func TestShouldFailoverLLMError_QuotaExceededDoesFailover(t *testing.T) {
	err := llm.ErrorFromHTTPStatus("openai", 400, "quota exceeded for account", nil, nil)
	if !shouldFailoverLLMError(err) {
		t.Fatalf("quota failures should trigger failover")
	}
}

func TestShouldFailoverLLMError_TurnLimitDoesNotFailover(t *testing.T) {
	if shouldFailoverLLMError(agent.ErrTurnLimit) {
		t.Fatalf("agent.ErrTurnLimit should not trigger failover")
	}
	if shouldFailoverLLMError(fmt.Errorf("wrapped: %w", agent.ErrTurnLimit)) {
		t.Fatalf("wrapped turn limit should not trigger failover")
	}
	if shouldFailoverLLMError(fmt.Errorf("turn limit reached")) {
		t.Fatalf("legacy turn-limit string should not trigger failover")
	}
}

func TestShouldFailoverLLMError_GetwdBootstrapErrorDoesNotFailover(t *testing.T) {
	err := fmt.Errorf("tool read_file schema: getwd: no such file or directory")
	if shouldFailoverLLMError(err) {
		t.Fatalf("getwd bootstrap errors should not trigger failover")
	}
}

func TestAgentLoopProviderOptions_CodexAppServer_UsesFullAutonomousPermissions(t *testing.T) {
	got := agentLoopProviderOptions("codex_app_server", "/tmp/worktree")
	if len(got) != 1 {
		t.Fatalf("provider options length=%d want 1", len(got))
	}
	raw, ok := got["codex_app_server"]
	if !ok {
		t.Fatalf("missing codex_app_server provider options: %#v", got)
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("codex_app_server options type=%T want map[string]any", raw)
	}
	if gotCwd := fmt.Sprint(opts["cwd"]); gotCwd != "/tmp/worktree" {
		t.Fatalf("cwd=%q want %q", gotCwd, "/tmp/worktree")
	}
	if gotApproval := fmt.Sprint(opts["approvalPolicy"]); gotApproval != "never" {
		t.Fatalf("approvalPolicy=%q want %q", gotApproval, "never")
	}
	if gotSandbox := fmt.Sprint(opts["sandbox"]); gotSandbox != "danger-full-access" {
		t.Fatalf("sandbox=%q want %q", gotSandbox, "danger-full-access")
	}
	rawSandboxPolicy, ok := opts["sandboxPolicy"]
	if !ok {
		t.Fatalf("missing sandboxPolicy in codex options: %#v", opts)
	}
	sandboxPolicy, ok := rawSandboxPolicy.(map[string]any)
	if !ok {
		t.Fatalf("sandboxPolicy type=%T want map[string]any", rawSandboxPolicy)
	}
	if gotType := fmt.Sprint(sandboxPolicy["type"]); gotType != "dangerFullAccess" {
		t.Fatalf("sandboxPolicy.type=%q want %q", gotType, "dangerFullAccess")
	}
}

func TestAgentLoopProviderOptions_Cerebras_PreservesReasoningHistory(t *testing.T) {
	got := agentLoopProviderOptions("cerebras", "")
	raw, ok := got["cerebras"]
	if !ok {
		t.Fatalf("missing cerebras provider options: %#v", got)
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("cerebras options type=%T want map[string]any", raw)
	}
	clearThinking, ok := opts["clear_thinking"].(bool)
	if !ok {
		t.Fatalf("clear_thinking type=%T want bool", opts["clear_thinking"])
	}
	if clearThinking {
		t.Fatalf("clear_thinking=%v want false", clearThinking)
	}
}

func TestAgentLoopProviderOptions_CodexAppServer_OmitsCwdWhenWorktreeEmpty(t *testing.T) {
	got := agentLoopProviderOptions("codex-app-server", "")
	raw, ok := got["codex_app_server"]
	if !ok {
		t.Fatalf("missing codex_app_server provider options: %#v", got)
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("codex_app_server options type=%T want map[string]any", raw)
	}
	if _, exists := opts["cwd"]; exists {
		t.Fatalf("expected cwd to be omitted when worktreeDir is empty: %#v", opts["cwd"])
	}
}
