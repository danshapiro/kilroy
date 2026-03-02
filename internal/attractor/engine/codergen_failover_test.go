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

func TestCodergenRouter_WithFailoverText_FailsOverToDifferentProvider(t *testing.T) {
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
	r := NewCodergenRouterWithRuntimes(cfg, catalog, runtimes)

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

	txt, used, err := r.withFailoverText(ctx, nil, node, client, "openai", "gpt-5.2-codex", func(prov string, mid string) (string, error) {
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

func TestCodergenRouter_WithFailoverText_AppliesForceModelToFailoverProvider(t *testing.T) {
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
	r := NewCodergenRouterWithRuntimes(cfg, catalog, runtimes)
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

	txt, used, err := r.withFailoverText(ctx, execCtx, node, client, "openai", "gpt-5.2-codex", func(prov string, mid string) (string, error) {
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

func TestCodergenRouter_WithFailoverText_ExplicitEmptyFailoverDoesNotFallback(t *testing.T) {
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
	r := NewCodergenRouterWithRuntimes(cfg, nil, runtimes)

	client := llm.NewClient()
	client.Register(&okAdapter{name: "openai"})
	client.Register(&okAdapter{name: "anthropic"})

	attemptedAnthropic := false
	_, _, err = r.withFailoverText(context.Background(), nil, &model.Node{ID: "n1"}, client, "openai", "gpt-5.2-codex", func(prov string, mid string) (string, error) {
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

func TestCodergenRouter_WithFailoverText_OmittedFailoverDoesNotFallback(t *testing.T) {
	cfg := &RunConfigFile{Version: 1}
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
		"google": {Backend: BackendAPI},
	}
	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		t.Fatalf("resolveProviderRuntimes: %v", err)
	}
	r := NewCodergenRouterWithRuntimes(cfg, nil, runtimes)

	attempted := []string{}
	_, used, err := r.withFailoverText(context.Background(), nil, &model.Node{ID: "n1"}, llm.NewClient(), "openai", "gpt-5.2-codex", func(provider, model string) (string, error) {
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
	got := pickFailoverModelFromRuntime("zai", rt, catalog, "gpt-5.2-codex")
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
	got := pickFailoverModelFromRuntime("kimi", rt, nil, "gpt-5.2-codex")
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
	r := NewCodergenRouterWithRuntimes(&RunConfigFile{}, nil, map[string]ProviderRuntime{
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
