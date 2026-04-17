package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKimiCodingAndZai_APIIntegration(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writeProviderCatalogForTest(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	seenPaths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPaths[r.URL.Path]++
		mu.Unlock()
		switch r.URL.Path {
		case "/coding/v1/messages":
			body := decodeJSONBody(t, r)
			if !isKimiCodingContractRequest(body) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"kimi requires stream=true and max_tokens>=16000"}}`))
				return
			}
			writeAnthropicStreamOK(w, "ok")
		case "/api/coding/paas/v4/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"x","model":"minimax-m2.5","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	runCase := func(provider, model, keyEnv, baseURL string) {
		t.Helper()
		cfg := &RunConfigFile{Version: 1}
		cfg.Repo.Path = repo
		cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
		cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
		cfg.ModelDB.OpenRouterModelInfoPath = pinned
		cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
		cfg.Git.RunBranchPrefix = "attractor/run"
		cfg.LLM.Providers = map[string]ProviderConfig{
			provider: {
				Backend:  BackendAPI,
				Failover: []string{},
				API: ProviderAPIConfig{
					APIKeyEnv: keyEnv,
					BaseURL:   baseURL,
				},
			},
		}
		t.Setenv(keyEnv, "k")

		dot := []byte(fmt.Sprintf(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=%s, llm_model=%s, agent_mode=one_shot, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`, provider, model))

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "kz-" + provider, LogsRoot: logsRoot})
		if err != nil {
			t.Fatalf("%s run failed: %v", provider, err)
		}
	}

	runCase("kimi", "kimi-k2.5", "KIMI_API_KEY", srv.URL+"/coding")
	runCase("zai", "glm-4.7", "ZAI_API_KEY", srv.URL)
	runCase("minimax", "minimax-m2.5", "MINIMAX_API_KEY", srv.URL)

	mu.Lock()
	defer mu.Unlock()
	if seenPaths["/coding/v1/messages"] == 0 {
		t.Fatalf("missing kimi coding messages call: %v", seenPaths)
	}
	if seenPaths["/api/coding/paas/v4/chat/completions"] == 0 {
		t.Fatalf("missing zai chat-completions call: %v", seenPaths)
	}
	if seenPaths["/v1/chat/completions"] == 0 {
		t.Fatalf("missing minimax chat-completions call: %v", seenPaths)
	}
}

func TestKimiAgentLoop_UsesNativeKimiProviderRouting(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writeProviderCatalogForTest(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	seenPaths := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenPaths[r.URL.Path]++
		mu.Unlock()

		switch r.URL.Path {
		case "/coding/v1/messages":
			body := decodeJSONBody(t, r)
			if !isKimiCodingContractRequest(body) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"kimi requires stream=true and max_tokens>=16000"}}`))
				return
			}
			writeAnthropicStreamOK(w, "ok")
		case "/v1/responses":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"model_not_found","message":"The requested model 'kimi-k2.5' does not exist.","param":"model"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {
			Backend:  BackendAPI,
			Failover: []string{},
			API: ProviderAPIConfig{
				APIKeyEnv: "KIMI_API_KEY",
				BaseURL:   srv.URL + "/coding",
			},
		},
	}
	cfg.LLM.CLIProfile = "real"

	t.Setenv("KIMI_API_KEY", "k")
	// Also configure OpenAI so this test catches accidental profile-family routing.
	t.Setenv("OPENAI_API_KEY", "openai-k")
	t.Setenv("OPENAI_BASE_URL", srv.URL)

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=kimi, llm_model=kimi-k2.5, agent_mode=agent_loop, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "kz-kimi-agent-loop", LogsRoot: logsRoot}); err != nil {
		t.Fatalf("kimi agent_loop run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if seenPaths["/coding/v1/messages"] == 0 {
		t.Fatalf("missing kimi coding messages call: %v", seenPaths)
	}
	if seenPaths["/v1/responses"] != 0 {
		t.Fatalf("unexpected openai responses call for kimi agent_loop: %v", seenPaths)
	}
}

func TestKimiCoding_APIIntegration_EnforcesStreamingAndMinMaxTokensContract(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writeProviderCatalogForTest(t)
	cxdbSrv := newCXDBTestServer(t)

	var seenContract bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/coding/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := decodeJSONBody(t, r)
		seenContract = isKimiCodingContractRequest(body)
		if !seenContract {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"kimi requires stream=true and max_tokens>=16000"}}`))
			return
		}
		writeAnthropicStreamOK(w, "ok")
	}))
	defer srv.Close()

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {
			Backend:  BackendAPI,
			Failover: []string{},
			API: ProviderAPIConfig{
				APIKeyEnv: "KIMI_API_KEY",
				BaseURL:   srv.URL + "/coding",
			},
		},
	}
	t.Setenv("KIMI_API_KEY", "k")

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider="kimi", llm_model="kimi-k2.5", agent_mode=one_shot, auto_status=true, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "kz-kimi-contract", LogsRoot: logsRoot}); err != nil {
		t.Fatalf("kimi contract run failed: %v", err)
	}
	if !seenContract {
		t.Fatalf("expected kimi request to enforce stream=true and max_tokens>=16000")
	}
}

func TestKimiAgentLoop_ToolRoundTrip_DoesNotDropToolResponses(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	pinned := writeProviderCatalogForTest(t)
	cxdbSrv := newCXDBTestServer(t)

	var mu sync.Mutex
	calls := 0
	secondRequestStatus := ""
	var secondRequestBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/coding/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body := decodeJSONBody(t, r)
		if !isKimiCodingContractRequest(body) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"kimi requires stream=true and max_tokens>=16000"}}`))
			return
		}

		mu.Lock()
		calls++
		callNum := calls
		mu.Unlock()

		switch callNum {
		case 1:
			writeAnthropicToolUseStartInputPlusDelta(w, "toolu_roundtrip_1")
		case 2:
			secondRequestBody = body
			ok, status := anthropicBodyHasSuccessfulToolResult(body, "toolu_roundtrip_1")
			mu.Lock()
			secondRequestStatus = status
			mu.Unlock()
			if !ok {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"an assistant message with 'tool_calls' must be followed by tool messages responding to each 'tool_call_id'. The following tool_call_ids did not have response messages: toolu_roundtrip_1"},"type":"error"}`))
				return
			}
			writeAnthropicStreamOK(w, "ok")
		default:
			writeAnthropicStreamOK(w, "ok")
		}
	}))
	defer srv.Close()

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"kimi": {
			Backend:  BackendAPI,
			Failover: []string{},
			API: ProviderAPIConfig{
				APIKeyEnv: "KIMI_API_KEY",
				BaseURL:   srv.URL + "/coding",
			},
		},
	}
	cfg.LLM.CLIProfile = "real"
	disablePromptProbe := false
	cfg.Preflight.PromptProbes.Enabled = &disablePromptProbe
	t.Setenv("KIMI_API_KEY", "k")

	dot := []byte(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=kimi, llm_model=kimi-k2.5, agent_mode=agent_loop, auto_status=true, prompt="use tools as needed and then finish"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "kz-kimi-roundtrip", LogsRoot: logsRoot}); err != nil {
		t.Fatalf("kimi roundtrip run failed: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("expected at least 2 kimi calls, got %d", calls)
	}
	if secondRequestStatus != "ok" {
		raw, _ := json.Marshal(secondRequestBody)
		t.Fatalf("expected successful tool result continuity on second request, got %q body=%s", secondRequestStatus, string(raw))
	}
}

func writeAnthropicToolUseStartInputPlusDelta(w http.ResponseWriter, toolID string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	write := func(event string, data string) {
		_, _ = fmt.Fprintf(w, "event: %s\n", event)
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if f != nil {
			f.Flush()
		}
	}
	write("content_block_start", fmt.Sprintf(`{"index":0,"content_block":{"type":"tool_use","id":%q,"name":"shell","input":{"command":"printf ok"}}}`, toolID))
	write("content_block_delta", `{"index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"printf ok\"}"}}`)
	write("content_block_stop", `{"index":0}`)
	write("message_delta", `{"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	write("message_stop", `{}`)
}

func anthropicBodyHasSuccessfulToolResult(body map[string]any, toolUseID string) (bool, string) {
	msgs, _ := body["messages"].([]any)
	if len(msgs) == 0 {
		return false, "missing messages"
	}
	observed := make([]string, 0, 8)
	for _, msgAny := range msgs {
		msg, _ := msgAny.(map[string]any)
		role, _ := msg["role"].(string)
		content, _ := msg["content"].([]any)
		for _, partAny := range content {
			part, _ := partAny.(map[string]any)
			typ, _ := part["type"].(string)
			id := fmt.Sprint(part["tool_use_id"])
			observed = append(observed, fmt.Sprintf("%s:%s:%s", role, typ, id))
			if typ != "tool_result" {
				continue
			}
			if id != toolUseID {
				continue
			}
			if isErr, _ := part["is_error"].(bool); isErr {
				return false, "tool_result marked is_error=true"
			}
			toolContent := fmt.Sprint(part["content"])
			if strings.Contains(toolContent, "invalid tool arguments JSON") {
				return false, "tool_result content indicates malformed tool arguments"
			}
			return true, "ok"
		}
	}
	return false, fmt.Sprintf("missing tool_result for tool_use_id (observed=%v)", observed)
}
