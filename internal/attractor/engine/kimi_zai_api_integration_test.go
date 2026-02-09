package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestKimiCodingAndZai_OpenAIChatCompletionsIntegration(t *testing.T) {
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
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/coding/v1/messages":
			_, _ = w.Write([]byte(`{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"kimi-for-coding","stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`))
		case "/api/coding/paas/v4/chat/completions":
			_, _ = w.Write([]byte(`{"id":"x","model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
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
				Backend: BackendAPI,
				API: ProviderAPIConfig{
					APIKeyEnv:     keyEnv,
					BaseURL:       baseURL,
				},
			},
		}
		t.Setenv(keyEnv, "k")

		dot := []byte(fmt.Sprintf(`
digraph G {
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=%s, llm_model=%s, codergen_mode=one_shot, auto_status=true, prompt="say hi"]
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

	mu.Lock()
	defer mu.Unlock()
	if seenPaths["/coding/v1/messages"] == 0 {
		t.Fatalf("missing kimi coding messages call: %v", seenPaths)
	}
	if seenPaths["/api/coding/paas/v4/chat/completions"] == 0 {
		t.Fatalf("missing zai chat-completions call: %v", seenPaths)
	}
}
