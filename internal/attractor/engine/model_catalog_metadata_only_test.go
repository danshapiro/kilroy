package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWithConfig_ModelCatalogIsMetadataOnly_DoesNotAffectProviderRouting(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	// Deliberately provide only an Anthropic entry for this model name to ensure
	// routing is driven by graph/node attributes, not catalog metadata.
	pinned := filepath.Join(t.TempDir(), "pinned.json")
	if err := os.WriteFile(pinned, []byte(`{"data":[{"id":"anthropic/gpt-5.2"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cxdbSrv := newCXDBTestServer(t)

	var openaiCalls atomic.Int32
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		openaiCalls.Add(1)
		_, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI},
	}
	cfg.ModelDB.LiteLLMCatalogPath = pinned
	cfg.ModelDB.LiteLLMCatalogUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.2, codergen_mode=one_shot, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "test-catalog-metadata-only", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}
	if got := openaiCalls.Load(); got == 0 {
		t.Fatalf("expected at least one OpenAI API call, got %d", got)
	}
}
