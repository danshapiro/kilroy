package engine

import (
	"context"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/agent"
)

func TestEmitCXDBToolTurns_EmitsAssistantToolCallAndToolResult(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	emitCXDBToolTurns(ctx, eng, "node_api", agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		Timestamp: time.Now(),
		Data: map[string]any{
			"text": "I will read the file.",
		},
	})
	emitCXDBToolTurns(ctx, eng, "node_api", agent.SessionEvent{
		Kind:      agent.EventToolCallStart,
		Timestamp: time.Now(),
		Data: map[string]any{
			"tool_name":      "Read",
			"call_id":        "toolu_123",
			"arguments_json": `{"file_path":"/tmp/a.txt"}`,
		},
	})
	emitCXDBToolTurns(ctx, eng, "node_api", agent.SessionEvent{
		Kind:      agent.EventToolCallEnd,
		Timestamp: time.Now(),
		Data: map[string]any{
			"tool_name":   "Read",
			"call_id":     "toolu_123",
			"full_output": "hello world",
			"is_error":    false,
		},
	})

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(turns))
	}

	if turns[0]["type_id"] != "com.kilroy.attractor.AssistantMessage" {
		t.Fatalf("turn[0] type_id: got %q", turns[0]["type_id"])
	}
	assistant := turns[0]["payload"].(map[string]any)
	if assistant["text"] != "I will read the file." {
		t.Fatalf("AssistantMessage text: got %q", assistant["text"])
	}
	if assistant["node_id"] != "node_api" {
		t.Fatalf("AssistantMessage node_id: got %q", assistant["node_id"])
	}
	if assistant["run_id"] != "test-run" {
		t.Fatalf("AssistantMessage run_id: got %q", assistant["run_id"])
	}
	if _, ok := assistant["timestamp_ms"]; !ok {
		t.Fatal("AssistantMessage missing timestamp_ms")
	}

	if turns[1]["type_id"] != "com.kilroy.attractor.ToolCall" {
		t.Fatalf("turn[1] type_id: got %q", turns[1]["type_id"])
	}
	toolCall := turns[1]["payload"].(map[string]any)
	if toolCall["tool_name"] != "Read" {
		t.Fatalf("ToolCall tool_name: got %q", toolCall["tool_name"])
	}
	if toolCall["call_id"] != "toolu_123" {
		t.Fatalf("ToolCall call_id: got %q", toolCall["call_id"])
	}

	if turns[2]["type_id"] != "com.kilroy.attractor.ToolResult" {
		t.Fatalf("turn[2] type_id: got %q", turns[2]["type_id"])
	}
	toolResult := turns[2]["payload"].(map[string]any)
	if toolResult["tool_name"] != "Read" {
		t.Fatalf("ToolResult tool_name: got %q", toolResult["tool_name"])
	}
	if toolResult["call_id"] != "toolu_123" {
		t.Fatalf("ToolResult call_id: got %q", toolResult["call_id"])
	}
	if toolResult["output"] != "hello world" {
		t.Fatalf("ToolResult output: got %q", toolResult["output"])
	}
	if toolResult["is_error"] != false {
		t.Fatalf("ToolResult is_error: got %v", toolResult["is_error"])
	}
}

func TestEmitCXDBToolTurns_AssistantTextFallbackForToolOnlyTurn(t *testing.T) {
	srv := newCXDBTestServer(t)
	eng := newTestEngineWithCXDB(t, srv)
	ctx := context.Background()

	emitCXDBToolTurns(ctx, eng, "node_api", agent.SessionEvent{
		Kind:      agent.EventAssistantTextEnd,
		Timestamp: time.Now(),
		Data: map[string]any{
			"text": "   ",
		},
	})

	turns := srv.Turns(eng.CXDB.ContextID)
	if len(turns) != 1 {
		t.Fatalf("expected 1 turn, got %d", len(turns))
	}
	if turns[0]["type_id"] != "com.kilroy.attractor.AssistantMessage" {
		t.Fatalf("type_id: got %q", turns[0]["type_id"])
	}
	payload := turns[0]["payload"].(map[string]any)
	if payload["text"] != "[tool_use]" {
		t.Fatalf("fallback text: got %q", payload["text"])
	}
}
