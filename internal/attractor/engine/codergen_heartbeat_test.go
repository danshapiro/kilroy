package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRunWithConfig_HeartbeatEmitsDuringCodergen(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Create a mock codex CLI that produces output slowly (to keep alive past heartbeat).
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"working"}]}}' >&1
# Keep running past the heartbeat interval.
sleep 3
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}' >&1
`), 0o755); err != nil {
		t.Fatal(err)
	}

	// Set heartbeat to 1s so we get at least 1-2 heartbeats during the 3s sleep.
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "1s")
	t.Setenv("KILROY_CODEX_IDLE_TIMEOUT", "10s")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test heartbeat"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="say hi"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "heartbeat-test", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Read progress.ndjson and look for stage_heartbeat events.
	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	data, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}

	heartbeats := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["event"] == "stage_heartbeat" {
			heartbeats++
			if ev["node_id"] != "a" {
				t.Errorf("heartbeat node_id: got %v want 'a'", ev["node_id"])
			}
			if _, ok := ev["elapsed_s"]; !ok {
				t.Error("heartbeat missing elapsed_s")
			}
			if _, ok := ev["stdout_bytes"]; !ok {
				t.Error("heartbeat missing stdout_bytes")
			}
		}
	}
	if heartbeats == 0 {
		t.Fatal("expected at least 1 stage_heartbeat event in progress.ndjson")
	}
	t.Logf("found %d heartbeat events", heartbeats)
}

func TestCodergenHeartbeatInterval_StallTimeoutScaling(t *testing.T) {
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "")
	if interval := codergenHeartbeatIntervalWithStallTimeout(900 * time.Millisecond); interval != 300*time.Millisecond {
		t.Fatalf("unexpected interval scaling: got %v want 300ms", interval)
	}
	if interval := codergenHeartbeatIntervalWithStallTimeout(100 * time.Millisecond); interval != codergenHeartbeatMinInterval {
		t.Fatalf("expected min clamp: got %v want %v", interval, codergenHeartbeatMinInterval)
	}
	if interval := codergenHeartbeatIntervalWithStallTimeout(30 * time.Minute); interval != codergenHeartbeatDefaultInterval {
		t.Fatalf("expected upper clamp: got %v want %v", interval, codergenHeartbeatDefaultInterval)
	}
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "37ms")
	if interval := codergenHeartbeatIntervalWithStallTimeout(900 * time.Millisecond); interval != 37*time.Millisecond {
		t.Fatalf("expected env override: got %v want 37ms", interval)
	}
}

func TestRunWithConfig_APIBackend_HeartbeatEmitsDuringAgentLoop(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Mock OpenAI server that takes 2 requests (tool call + final) with a delay.
	requestCount := 0
	var reqMu sync.Mutex
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reqMu.Lock()
		requestCount++
		n := requestCount
		reqMu.Unlock()

		// First request: return a shell tool call that sleeps briefly.
		if n == 1 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.4",
  "output": [{"type":"function_call","id":"call_1","name":"shell","arguments":"{\"command\":\"sleep 1\"}"}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
			return
		}
		// Second request onward: simulate API thinking time so the heartbeat
		// goroutine fires at least once before the session completes.
		time.Sleep(500 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_2",
  "model": "gpt-5.4",
  "output": [{"type":"message","content":[{"type":"output_text","text":"done"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "200ms")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test api heartbeat"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="run a command"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "api-heartbeat-test", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	data, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}

	heartbeats := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["event"] == "stage_heartbeat" && ev["node_id"] == "a" {
			heartbeats++
			if _, ok := ev["elapsed_s"]; !ok {
				t.Error("heartbeat missing elapsed_s")
			}
			if _, ok := ev["event_count"]; !ok {
				t.Error("heartbeat missing event_count")
			}
		}
	}
	if heartbeats == 0 {
		t.Fatal("expected at least 1 stage_heartbeat event during API agent_loop execution")
	}
	t.Logf("found %d API heartbeat events", heartbeats)
}

func TestRunWithConfig_APIBackend_SessionEventsPreventFalseStallWithoutHeartbeat(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	requestCount := 0
	var reqMu sync.Mutex
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		reqMu.Lock()
		requestCount++
		n := requestCount
		reqMu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch n {
		case 1:
			_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.4",
  "output": [{"type":"function_call","id":"call_1","name":"shell","arguments":"{\"command\":\"sleep 0.3\"}"}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
		case 2:
			_, _ = w.Write([]byte(`{
  "id": "resp_2",
  "model": "gpt-5.4",
  "output": [{"type":"function_call","id":"call_2","name":"shell","arguments":"{\"command\":\"sleep 0.3\"}"}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
		case 3:
			_, _ = w.Write([]byte(`{
  "id": "resp_3",
  "model": "gpt-5.4",
  "output": [{"type":"function_call","id":"call_3","name":"shell","arguments":"{\"command\":\"sleep 0.3\"}"}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
		default:
			_, _ = w.Write([]byte(`{
  "id": "resp_4",
  "model": "gpt-5.4",
  "output": [{"type":"message","content":[{"type":"output_text","text":"done"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
		}
	}))
	t.Cleanup(openaiSrv.Close)

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)
	// Intentionally larger than stall timeout so heartbeat events cannot mask
	// watchdog behavior in this test.
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "10s")

	stallTimeout := 700
	stallCheck := 50
	disableProbe := false
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.RuntimePolicy.StallTimeoutMS = &stallTimeout
	cfg.RuntimePolicy.StallCheckIntervalMS = &stallCheck
	cfg.Preflight.PromptProbes.Enabled = &disableProbe

	dot := []byte(`
digraph G {
  graph [goal="session events should prevent false stall timeout"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="run a few commands"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "api-session-activity-test", LogsRoot: logsRoot})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	data, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}

	var stageSucceeded bool
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["event"] == "stall_watchdog_timeout" {
			t.Fatalf("unexpected stall watchdog timeout during active session: %+v", ev)
		}
		if ev["event"] == "stage_attempt_end" && ev["node_id"] == "a" && ev["status"] == "success" {
			stageSucceeded = true
		}
	}
	if !stageSucceeded {
		t.Fatal("expected stage a to complete successfully")
	}
}

// TestRunWithConfig_APIBackend_StallWatchdogFiresDespiteHeartbeatGoroutine verifies
// that the stall watchdog still fires when the API agent_loop session is truly
// stalled (no new session events) even though the heartbeat goroutine is running.
// Heartbeat events are emitted unconditionally for observability but use
// appendProgressLivenessOnly when no new events are produced, which does not
// reset the stall watchdog timer.
func TestRunWithConfig_APIBackend_StallWatchdogFiresDespiteHeartbeatGoroutine(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Mock OpenAI server that hangs on the first request, simulating a stalled
	// API call where no session events are produced.
	stallRelease := make(chan struct{})
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		<-stallRelease
	}))
	t.Cleanup(openaiSrv.Close)
	t.Cleanup(func() { close(stallRelease) })

	t.Setenv("OPENAI_API_KEY", "k")
	t.Setenv("OPENAI_BASE_URL", openaiSrv.URL)
	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "100ms")

	stallTimeout := 800
	stallCheck := 50
	disableProbe := false
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendAPI, Failover: []string{}},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.RuntimePolicy.StallTimeoutMS = &stallTimeout
	cfg.RuntimePolicy.StallCheckIntervalMS = &stallCheck
	cfg.Preflight.PromptProbes.Enabled = &disableProbe

	dot := []byte(`
digraph G {
  graph [goal="test stall detection with api heartbeat"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, auto_status=true, prompt="do something"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "api-stall-test", LogsRoot: logsRoot})
	if err == nil {
		t.Fatal("expected stall watchdog timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stall watchdog") {
		t.Fatalf("expected stall watchdog error, got: %v", err)
	}
	t.Logf("stall watchdog fired as expected: %v", err)
}

// TestRunWithConfig_CLIBackend_StallWatchdogFiresDespiteHeartbeatGoroutine verifies
// that the stall watchdog still fires when the CLI codergen process is truly
// stalled (no stdout/stderr output) even though the heartbeat goroutine is running.
// Heartbeat events are emitted unconditionally for observability but use
// appendProgressLivenessOnly when no output growth is detected, which does not
// reset the stall watchdog timer.
func TestRunWithConfig_CLIBackend_StallWatchdogFiresDespiteHeartbeatGoroutine(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Create a mock codex CLI that hangs without producing any output.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte("#!/usr/bin/env bash\nsleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "100ms")
	t.Setenv("KILROY_CODEX_IDLE_TIMEOUT", "60s")

	stallTimeout := 500
	stallCheck := 50
	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"
	cfg.RuntimePolicy.StallTimeoutMS = &stallTimeout
	cfg.RuntimePolicy.StallCheckIntervalMS = &stallCheck

	dot := []byte(`
digraph G {
  graph [goal="test cli stall detection with heartbeat"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="do something"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "cli-stall-test", LogsRoot: logsRoot, AllowTestShim: true})
	if err == nil {
		t.Fatal("expected stall watchdog timeout error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "stall watchdog") {
		t.Fatalf("expected stall watchdog error, got: %v", err)
	}
	t.Logf("stall watchdog fired as expected: %v", err)
}

func TestRunWithConfig_HeartbeatEmitsDuringQuietPeriods(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()

	pinned := writePinnedCatalog(t)
	cxdbSrv := newCXDBTestServer(t)

	// Create a mock codex CLI that produces initial output, then goes quiet,
	// then produces more output. The quiet period should still produce heartbeats.
	cli := filepath.Join(t.TempDir(), "codex")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"starting"}]}}' >&1
# Quiet period: no output for 3 seconds.
sleep 3
echo '{"item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}}' >&1
`), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("KILROY_CODERGEN_HEARTBEAT_INTERVAL", "1s")
	t.Setenv("KILROY_CODEX_IDLE_TIMEOUT", "10s")

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"openai": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = pinned
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := []byte(`
digraph G {
  graph [goal="test quiet period heartbeats"]
  start [shape=Mdiamond]
  exit  [shape=Msquare]
  a [shape=box, llm_provider=openai, llm_model=gpt-5.4, prompt="do something quiet"]
  start -> a -> exit
}
`)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "quiet-heartbeat-test", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	progressPath := filepath.Join(res.LogsRoot, "progress.ndjson")
	data, err := os.ReadFile(progressPath)
	if err != nil {
		t.Fatalf("read progress.ndjson: %v", err)
	}

	heartbeats := 0
	var hasQuietHeartbeat bool
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev["event"] == "stage_heartbeat" && ev["node_id"] == "a" {
			heartbeats++
			if _, ok := ev["since_last_output_s"]; !ok {
				t.Error("heartbeat missing since_last_output_s field")
			}
			sinceOutput, _ := ev["since_last_output_s"].(float64)
			if sinceOutput >= 1 {
				hasQuietHeartbeat = true
			}
		}
	}
	if heartbeats < 2 {
		t.Fatalf("expected at least 2 heartbeat events (some during quiet period), got %d", heartbeats)
	}
	if !hasQuietHeartbeat {
		t.Fatal("expected at least one heartbeat with since_last_output_s >= 1 (quiet period heartbeat)")
	}
	t.Logf("found %d heartbeat events, quiet period heartbeats present", heartbeats)
}

func TestRunWithConfig_HeartbeatStopsAfterProcessExit(t *testing.T) {
	events := runHeartbeatFixture(t)
	endIdx := findEventIndex(events, "stage_attempt_end", "a")
	if endIdx < 0 {
		t.Fatal("missing stage_attempt_end for node a")
	}
	for _, ev := range events[endIdx+1:] {
		if ev["event"] == "stage_heartbeat" && ev["node_id"] == "a" {
			t.Fatalf("unexpected heartbeat after attempt end: %+v", ev)
		}
	}
}
