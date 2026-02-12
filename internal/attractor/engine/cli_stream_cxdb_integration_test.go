package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCLIStreamCXDB_DecomposesConversationTurns(t *testing.T) {
	repo := initTestRepo(t)
	logsRoot := t.TempDir()
	catalog := writeCatalogForPreflight(t, `{
  "data": [
    {"id": "anthropic/claude-sonnet-4-20250514"}
  ]
}`)
	cxdbSrv := newCXDBTestServer(t)

	// Fake claude CLI that emits realistic stream-json: two assistant messages,
	// one with a tool_use, and a user message with the tool_result.
	cli := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(cli, []byte(`#!/usr/bin/env bash
set -euo pipefail
if [[ "${1:-}" == "--help" ]]; then
cat <<'EOF'
Usage: claude -p --output-format stream-json --verbose --dangerously-skip-permissions --model MODEL
EOF
exit 0
fi

# Find status.json path from args
status_path=""
for arg in "$@"; do
  if [[ "$arg" == *.json ]] && [[ "$status_path_next" == "1" ]]; then
    status_path="$arg"
    break
  fi
  status_path_next=0
done
# Write status.json in the working directory
cat > status.json <<'JSON'
{"status":"success","notes":"ok"}
JSON

# Assistant message 1: text + tool_use
echo '{"type":"system","subtype":"init","session_id":"s1"}'
echo '{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_001","role":"assistant","content":[{"type":"text","text":"Let me read the README."},{"type":"tool_use","id":"toolu_001","name":"Read","input":{"file_path":"/tmp/README.md"}}],"usage":{"input_tokens":1500,"output_tokens":42}},"session_id":"s1","uuid":"u1"}'

# User message: tool_result
echo '{"type":"user","message":{"role":"user","content":[{"tool_use_id":"toolu_001","type":"tool_result","content":"# Hello World\nThis is a test."}]},"session_id":"s1","uuid":"u2"}'

# Assistant message 2: text only
echo '{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","id":"msg_002","role":"assistant","content":[{"type":"text","text":"The README looks good. No changes needed."}],"usage":{"input_tokens":2000,"output_tokens":15}},"session_id":"s1","uuid":"u3"}'

echo '{"type":"result","subtype":"success","result":"done","session_id":"s1"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &RunConfigFile{Version: 1}
	cfg.Repo.Path = repo
	cfg.CXDB.BinaryAddr = cxdbSrv.BinaryAddr()
	cfg.CXDB.HTTPBaseURL = cxdbSrv.URL()
	cfg.LLM.CLIProfile = "test_shim"
	cfg.LLM.Providers = map[string]ProviderConfig{
		"anthropic": {Backend: BackendCLI, Executable: cli},
	}
	cfg.ModelDB.OpenRouterModelInfoPath = catalog
	cfg.ModelDB.OpenRouterModelInfoUpdatePolicy = "pinned"
	cfg.Git.RunBranchPrefix = "attractor/run"

	dot := singleProviderDot("anthropic", "claude-sonnet-4-20250514")

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, err := RunWithConfig(ctx, dot, cfg, RunOptions{RunID: "cli-stream-cxdb", LogsRoot: logsRoot, AllowTestShim: true})
	if err != nil {
		t.Fatalf("RunWithConfig: %v", err)
	}

	// Verify blob capture is preserved: stdout.log and events.ndjson should exist.
	for _, name := range []string{"stdout.log", "events.ndjson"} {
		p := filepath.Join(res.LogsRoot, "a", name)
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}

	// Find the context ID from the result.
	contextIDs := cxdbSrv.ContextIDs()
	if len(contextIDs) == 0 {
		t.Fatal("expected at least one CXDB context")
	}

	// Collect all turns across all contexts (RunWithConfig may fork).
	var allTurns []map[string]any
	for _, cid := range contextIDs {
		allTurns = append(allTurns, cxdbSrv.Turns(cid)...)
	}

	// Count decomposed conversation turns by type.
	typeCounts := map[string]int{}
	for _, turn := range allTurns {
		typeID, _ := turn["type_id"].(string)
		typeCounts[typeID]++
	}

	// Expect 2 AssistantMessage turns (one per assistant stream event).
	if got := typeCounts["com.kilroy.attractor.AssistantMessage"]; got != 2 {
		t.Fatalf("AssistantMessage turns: got %d want 2 (types: %v)", got, typeCounts)
	}

	// Expect 1 ToolCall turn (from the first assistant message's tool_use).
	if got := typeCounts["com.kilroy.attractor.ToolCall"]; got != 1 {
		t.Fatalf("ToolCall turns: got %d want 1 (types: %v)", got, typeCounts)
	}

	// Expect 1 ToolResult turn (from the user message's tool_result).
	if got := typeCounts["com.kilroy.attractor.ToolResult"]; got != 1 {
		t.Fatalf("ToolResult turns: got %d want 1 (types: %v)", got, typeCounts)
	}

	// Verify AssistantMessage field population.
	for _, turn := range allTurns {
		if turn["type_id"] != "com.kilroy.attractor.AssistantMessage" {
			continue
		}
		payload, _ := turn["payload"].(map[string]any)
		if payload == nil {
			t.Fatal("AssistantMessage payload is nil")
		}
		if model, _ := payload["model"].(string); model != "claude-sonnet-4-5-20250929" {
			t.Fatalf("AssistantMessage model: got %q", model)
		}
		if payload["timestamp_ms"] == nil {
			t.Fatal("AssistantMessage missing timestamp_ms")
		}
		break // check first one only
	}

	// Verify ToolCall has the correct tool name and call_id.
	for _, turn := range allTurns {
		if turn["type_id"] != "com.kilroy.attractor.ToolCall" {
			continue
		}
		payload, _ := turn["payload"].(map[string]any)
		if payload == nil {
			t.Fatal("ToolCall payload is nil")
		}
		if name, _ := payload["tool_name"].(string); name != "Read" {
			t.Fatalf("ToolCall tool_name: got %q want Read", name)
		}
		if callID, _ := payload["call_id"].(string); callID != "toolu_001" {
			t.Fatalf("ToolCall call_id: got %q want toolu_001", callID)
		}
		break
	}

	// Verify ToolResult propagates tool_name from the callMap.
	for _, turn := range allTurns {
		if turn["type_id"] != "com.kilroy.attractor.ToolResult" {
			continue
		}
		payload, _ := turn["payload"].(map[string]any)
		if payload == nil {
			t.Fatal("ToolResult payload is nil")
		}
		if name, _ := payload["tool_name"].(string); name != "Read" {
			t.Fatalf("ToolResult tool_name: got %q want Read (propagated from callMap)", name)
		}
		break
	}

	// Existing structural turns should still be present.
	if typeCounts["com.kilroy.attractor.RunStarted"] < 1 {
		t.Fatalf("missing RunStarted turn (types: %v)", typeCounts)
	}
	if typeCounts["com.kilroy.attractor.StageStarted"] < 1 {
		t.Fatalf("missing StageStarted turn (types: %v)", typeCounts)
	}
}
