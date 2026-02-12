package engine

import (
	"context"
	"fmt"
	"testing"

	"github.com/strongdm/kilroy/internal/cxdb"
)

func TestEmitCXDBCLIStreamEvent_AssistantMessage(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	ev := &cliStreamEvent{
		Type: "assistant",
		Message: &cliMessage{
			Model: "claude-sonnet-4-5-20250929",
			Role:  "assistant",
			Content: []cliContentBlock{
				{Type: "text", Text: "Here is the answer."},
			},
			Usage: &cliUsage{InputTokens: 1000, OutputTokens: 50},
		},
	}

	emitCXDBCLIStreamEvent(ctx, eng, "node_a", ev, nil)

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	turn := turns[0]
	if turn["type_id"] != "com.kilroy.attractor.AssistantMessage" {
		t.Fatalf("type_id: got %q", turn["type_id"])
	}
	payload := turn["payload"].(map[string]any)
	if payload["model"] != "claude-sonnet-4-5-20250929" {
		t.Fatalf("model: got %q", payload["model"])
	}
	if payload["text"] != "Here is the answer." {
		t.Fatalf("text: got %q", payload["text"])
	}
	if payload["node_id"] != "node_a" {
		t.Fatalf("node_id: got %q", payload["node_id"])
	}
}

func TestEmitCXDBCLIStreamEvent_AssistantWithToolUse(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	ev := &cliStreamEvent{
		Type: "assistant",
		Message: &cliMessage{
			Model: "claude-sonnet-4-5-20250929",
			Role:  "assistant",
			Content: []cliContentBlock{
				{Type: "text", Text: "Let me read that."},
				{Type: "tool_use", ID: "toolu_abc", Name: "Read", Input: map[string]any{"file_path": "/tmp/f.go"}},
				{Type: "tool_use", ID: "toolu_def", Name: "Bash", Input: map[string]any{"command": "ls"}},
			},
			Usage: &cliUsage{InputTokens: 2000, OutputTokens: 100},
		},
	}

	callMap := map[string]string{}
	emitCXDBCLIStreamEvent(ctx, eng, "node_b", ev, callMap)

	turns := srv.Turns(eng.CXDB.ContextID)
	// 1 AssistantMessage + 2 ToolCall = 3 turns
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}

	// First turn: AssistantMessage
	if turns[0]["type_id"] != "com.kilroy.attractor.AssistantMessage" {
		t.Fatalf("turn[0] type_id: got %q", turns[0]["type_id"])
	}
	p0 := turns[0]["payload"].(map[string]any)
	// JSON round-trips numeric values as float64.
	if fmt.Sprint(p0["tool_use_count"]) != "2" {
		t.Fatalf("tool_use_count: got %v", p0["tool_use_count"])
	}

	// Second turn: ToolCall for Read
	if turns[1]["type_id"] != "com.kilroy.attractor.ToolCall" {
		t.Fatalf("turn[1] type_id: got %q", turns[1]["type_id"])
	}
	p1 := turns[1]["payload"].(map[string]any)
	if p1["tool_name"] != "Read" {
		t.Fatalf("turn[1] tool_name: got %q", p1["tool_name"])
	}

	// Third turn: ToolCall for Bash
	if turns[2]["type_id"] != "com.kilroy.attractor.ToolCall" {
		t.Fatalf("turn[2] type_id: got %q", turns[2]["type_id"])
	}
	p2 := turns[2]["payload"].(map[string]any)
	if p2["tool_name"] != "Bash" {
		t.Fatalf("turn[2] tool_name: got %q", p2["tool_name"])
	}

	// callMap should have been populated
	if callMap["toolu_abc"] != "Read" {
		t.Fatalf("callMap[toolu_abc]: got %q", callMap["toolu_abc"])
	}
	if callMap["toolu_def"] != "Bash" {
		t.Fatalf("callMap[toolu_def]: got %q", callMap["toolu_def"])
	}
}

func TestEmitCXDBCLIStreamEvent_ToolOnlyAssistantMessageGetsDescriptiveText(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	ev := &cliStreamEvent{
		Type: "assistant",
		Message: &cliMessage{
			Model: "claude-sonnet-4-5-20250929",
			Role:  "assistant",
			Content: []cliContentBlock{
				{Type: "tool_use", ID: "toolu_xyz", Name: "Write", Input: map[string]any{"file_path": "/tmp/out.txt"}},
			},
			Usage: &cliUsage{InputTokens: 500, OutputTokens: 30},
		},
	}

	emitCXDBCLIStreamEvent(ctx, eng, "node_e", ev, nil)

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) < 1 {
		t.Fatalf("expected at least 1 turn, got %d", len(turns))
	}
	p := turns[0]["payload"].(map[string]any)
	text, _ := p["text"].(string)
	if text == "" {
		t.Fatalf("expected non-empty text for tool-only assistant message, got empty")
	}
	if text != "[tool_use: Write]" {
		t.Fatalf("expected text '[tool_use: Write]', got %q", text)
	}
}

func TestEmitCXDBCLIStreamEvent_MultiToolOnlyAssistantMessageListsAll(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	ev := &cliStreamEvent{
		Type: "assistant",
		Message: &cliMessage{
			Model: "claude-sonnet-4-5-20250929",
			Role:  "assistant",
			Content: []cliContentBlock{
				{Type: "tool_use", ID: "toolu_1", Name: "Read", Input: map[string]any{}},
				{Type: "tool_use", ID: "toolu_2", Name: "Bash", Input: map[string]any{}},
			},
			Usage: &cliUsage{InputTokens: 500, OutputTokens: 30},
		},
	}

	emitCXDBCLIStreamEvent(ctx, eng, "node_f", ev, nil)

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) < 1 {
		t.Fatalf("expected at least 1 turn, got %d", len(turns))
	}
	p := turns[0]["payload"].(map[string]any)
	text, _ := p["text"].(string)
	if text != "[tool_use: Read, Bash]" {
		t.Fatalf("expected text '[tool_use: Read, Bash]', got %q", text)
	}
}

func TestEmitCXDBCLIStreamEvent_UserToolResult(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	callMap := map[string]string{
		"toolu_abc": "Read",
	}

	ev := &cliStreamEvent{
		Type: "user",
		Message: &cliMessage{
			Role: "user",
			Content: []cliContentBlock{
				{Type: "tool_result", ToolUseID: "toolu_abc", Content: "file contents here"},
			},
		},
	}

	emitCXDBCLIStreamEvent(ctx, eng, "node_c", ev, callMap)

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0]["type_id"] != "com.kilroy.attractor.ToolResult" {
		t.Fatalf("type_id: got %q", turns[0]["type_id"])
	}
	p := turns[0]["payload"].(map[string]any)
	if p["tool_name"] != "Read" {
		t.Fatalf("tool_name: got %q (expected from callMap)", p["tool_name"])
	}
	if p["call_id"] != "toolu_abc" {
		t.Fatalf("call_id: got %q", p["call_id"])
	}
}

func TestEmitCXDBCLIStreamEvent_SystemSkipped(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	ev := &cliStreamEvent{Type: "system"}
	emitCXDBCLIStreamEvent(ctx, eng, "node_d", ev, nil)

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) != 0 {
		t.Fatalf("expected 0 turns for system event, got %d", len(turns))
	}
}

func TestEmitCXDBCLIStreamEvent_NilSafety(t *testing.T) {
	ctx := context.Background()

	// nil engine
	emitCXDBCLIStreamEvent(ctx, nil, "node", &cliStreamEvent{Type: "assistant"}, nil)

	// engine with nil CXDB
	eng := &Engine{}
	emitCXDBCLIStreamEvent(ctx, eng, "node", &cliStreamEvent{Type: "assistant"}, nil)

	// nil event
	srv := newCXDBTestServer(t)
	eng2 := newTestEngineWithCXDB(t, srv)
	emitCXDBCLIStreamEvent(ctx, eng2, "node", nil, nil)

	turns := srv.Turns(eng2.CXDB.ContextID)
	if len(turns) != 0 {
		t.Fatalf("expected 0 turns, got %d", len(turns))
	}
}

// newTestEngineWithCXDB creates a minimal Engine with a CXDB sink for testing.
func newTestEngineWithCXDB(t *testing.T, srv *cxdbTestServer) *Engine {
	t.Helper()
	httpClient := cxdb.New(srv.URL())
	ctx := context.Background()
	ci, err := httpClient.CreateContext(ctx, "0")
	if err != nil {
		t.Fatalf("create cxdb context: %v", err)
	}
	sink := NewCXDBSink(httpClient, nil, "test-run", ci.ContextID, ci.HeadTurnID, "test-bundle")
	return &Engine{
		Options: RunOptions{RunID: "test-run"},
		CXDB:    sink,
	}
}
