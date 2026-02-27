package codexappserver

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/llm"
)

func streamNotification(method string, params map[string]any) map[string]any {
	return map[string]any{"method": method, "params": params}
}

func collectStreamEvents(events []map[string]any) []llm.StreamEvent {
	in := make(chan map[string]any, len(events))
	for _, event := range events {
		in <- event
	}
	close(in)

	out := make([]llm.StreamEvent, 0, 16)
	for event := range translateStream(in) {
		out = append(out, event)
	}
	return out
}

func TestTranslateStream_TextAndFinishUsage(t *testing.T) {
	events := collectStreamEvents([]map[string]any{
		streamNotification("turn/started", map[string]any{"turn": map[string]any{"id": "turn_1", "status": "inProgress", "items": []any{}}}),
		streamNotification("item/agentMessage/delta", map[string]any{"itemId": "agent_1", "delta": "Hello"}),
		streamNotification("item/agentMessage/delta", map[string]any{"itemId": "agent_1", "delta": " world"}),
		streamNotification("item/completed", map[string]any{"item": map[string]any{"id": "agent_1", "type": "agentMessage", "text": "Hello world"}}),
		streamNotification("thread/tokenUsage/updated", map[string]any{"tokenUsage": map[string]any{"last": map[string]any{"inputTokens": 12, "outputTokens": 8, "totalTokens": 20, "reasoningOutputTokens": 2, "cachedInputTokens": 3}}}),
		streamNotification("turn/completed", map[string]any{"turn": map[string]any{"id": "turn_1", "status": "completed", "items": []any{}}}),
	})

	if len(events) == 0 {
		t.Fatalf("expected events")
	}
	if events[0].Type != llm.StreamEventStreamStart {
		t.Fatalf("first event type: got %q want %q", events[0].Type, llm.StreamEventStreamStart)
	}

	text := ""
	var finish *llm.StreamEvent
	for idx := range events {
		event := events[idx]
		if event.Type == llm.StreamEventTextDelta {
			text += event.Delta
		}
		if event.Type == llm.StreamEventFinish {
			finish = &events[idx]
		}
	}
	if text != "Hello world" {
		t.Fatalf("text delta mismatch: got %q want %q", text, "Hello world")
	}
	if finish == nil {
		t.Fatalf("expected finish event")
	}
	if finish.FinishReason == nil || finish.FinishReason.Reason != llm.FinishReasonStop {
		t.Fatalf("finish reason mismatch: %+v", finish.FinishReason)
	}
	if finish.Usage == nil || finish.Usage.TotalTokens != 20 {
		t.Fatalf("finish usage mismatch: %+v", finish.Usage)
	}
}

func TestTranslateStream_ParsesToolCallProtocol(t *testing.T) {
	events := collectStreamEvents([]map[string]any{
		streamNotification("turn/started", map[string]any{"turn": map[string]any{"id": "turn_2", "status": "inProgress", "items": []any{}}}),
		streamNotification("item/agentMessage/delta", map[string]any{"itemId": "agent_2", "delta": "Lead [[TOOL_CALL]]{\"id\":\"call_abc\",\"name\":\"lookup\",\"arguments\":{\"x\":1}}[[/TOOL_CALL]] tail"}),
		streamNotification("item/completed", map[string]any{"item": map[string]any{"id": "agent_2", "type": "agentMessage", "text": ""}}),
		streamNotification("turn/completed", map[string]any{"turn": map[string]any{"id": "turn_2", "status": "completed", "items": []any{}}}),
	})

	seenStart := false
	seenDelta := false
	seenEnd := false
	finish := llm.FinishReason{}
	for _, event := range events {
		switch event.Type {
		case llm.StreamEventToolCallStart:
			seenStart = true
			if event.ToolCall == nil || event.ToolCall.ID != "call_abc" || event.ToolCall.Name != "lookup" {
				t.Fatalf("tool call start mismatch: %+v", event.ToolCall)
			}
		case llm.StreamEventToolCallDelta:
			seenDelta = true
			if event.ToolCall == nil || string(event.ToolCall.Arguments) != `{"x":1}` {
				t.Fatalf("tool call delta mismatch: %+v", event.ToolCall)
			}
		case llm.StreamEventToolCallEnd:
			seenEnd = true
		case llm.StreamEventFinish:
			if event.FinishReason != nil {
				finish = *event.FinishReason
			}
		}
	}
	if !seenStart || !seenDelta || !seenEnd {
		t.Fatalf("tool call events missing: start=%t delta=%t end=%t", seenStart, seenDelta, seenEnd)
	}
	if finish.Reason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason mismatch: got %q want %q", finish.Reason, llm.FinishReasonToolCalls)
	}
}

func TestTranslateStream_FailedTurnEmitsErrorAndFinish(t *testing.T) {
	events := collectStreamEvents([]map[string]any{
		streamNotification("turn/started", map[string]any{"turn": map[string]any{"id": "turn_3", "status": "inProgress", "items": []any{}}}),
		streamNotification("error", map[string]any{"error": map[string]any{"message": "upstream overloaded"}}),
		streamNotification("turn/completed", map[string]any{"turn": map[string]any{"id": "turn_3", "status": "failed", "error": map[string]any{"message": "turn failed hard"}, "items": []any{}}}),
	})

	errorCount := 0
	finishCount := 0
	for _, event := range events {
		if event.Type == llm.StreamEventError {
			errorCount++
		}
		if event.Type == llm.StreamEventFinish {
			finishCount++
			if event.FinishReason == nil || event.FinishReason.Reason != llm.FinishReasonError {
				t.Fatalf("finish reason mismatch: %+v", event.FinishReason)
			}
		}
	}
	if errorCount != 2 {
		t.Fatalf("error count: got %d want 2", errorCount)
	}
	if finishCount != 1 {
		t.Fatalf("finish count: got %d want 1", finishCount)
	}
}

func TestTranslateStream_ProviderEventPassthrough(t *testing.T) {
	events := collectStreamEvents([]map[string]any{
		streamNotification("model/rerouted", map[string]any{"fromModel": "codex-mini", "toModel": "codex-pro"}),
	})
	if len(events) != 1 {
		t.Fatalf("event count: got %d want 1", len(events))
	}
	if events[0].Type != llm.StreamEventProviderEvent {
		t.Fatalf("event type: got %q want %q", events[0].Type, llm.StreamEventProviderEvent)
	}
	if events[0].EventType != "model/rerouted" {
		t.Fatalf("event_type: got %q want %q", events[0].EventType, "model/rerouted")
	}
}
