package llm

import (
	"encoding/json"
	"testing"
)

func TestStreamAccumulator_FinishWithResponse_UsesIt(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t", Delta: "ignored"})

	r := Response{Provider: "openai", Model: "m", Message: Assistant("Hello"), Finish: FinishReason{Reason: "stop"}}
	acc.Process(StreamEvent{Type: StreamEventFinish, Response: &r, FinishReason: &r.Finish, Usage: &r.Usage})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	if got.Provider != "openai" || got.Model != "m" || got.Text() != "Hello" {
		t.Fatalf("response: %+v", *got)
	}
}

func TestStreamAccumulator_NoFinishResponse_BuildsFromText(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{Type: StreamEventStreamStart})
	acc.Process(StreamEvent{Type: StreamEventTextStart, TextID: "t1"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "Hel"})
	acc.Process(StreamEvent{Type: StreamEventTextDelta, TextID: "t1", Delta: "lo"})
	acc.Process(StreamEvent{Type: StreamEventTextEnd, TextID: "t1"})

	if pr := acc.PartialResponse(); pr == nil || pr.Text() != "Hello" {
		if pr == nil {
			t.Fatalf("expected partial response, got nil")
		}
		t.Fatalf("partial text: %q", pr.Text())
	}

	f := FinishReason{Reason: "stop"}
	u := Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &f, Usage: &u})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	if got.Text() != "Hello" {
		t.Fatalf("text: %q", got.Text())
	}
	if got.Finish.Reason != "stop" {
		t.Fatalf("finish: %+v", got.Finish)
	}
	if got.Usage.TotalTokens != 3 {
		t.Fatalf("usage: %+v", got.Usage)
	}
}

func TestStreamAccumulator_NoFinishResponse_BuildsToolCalls(t *testing.T) {
	acc := NewStreamAccumulator()
	acc.Process(StreamEvent{
		Type: StreamEventToolCallStart,
		ToolCall: &ToolCallData{
			ID:   "call_1",
			Name: "write_file",
			Type: "function",
		},
	})
	acc.Process(StreamEvent{
		Type: StreamEventToolCallDelta,
		ToolCall: &ToolCallData{
			ID:        "call_1",
			Name:      "write_file",
			Type:      "function",
			Arguments: json.RawMessage(`{"path":"a.txt"}`),
		},
	})
	// Some providers send args again on TOOL_CALL_END; accumulator should avoid duplicating.
	acc.Process(StreamEvent{
		Type: StreamEventToolCallEnd,
		ToolCall: &ToolCallData{
			ID:        "call_1",
			Name:      "write_file",
			Type:      "function",
			Arguments: json.RawMessage(`{"path":"a.txt"}`),
		},
	})
	finish := FinishReason{Reason: FinishReasonToolCalls}
	acc.Process(StreamEvent{Type: StreamEventFinish, FinishReason: &finish})

	got := acc.Response()
	if got == nil {
		t.Fatalf("expected response")
	}
	calls := got.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("tool calls: got %d want 1 (%+v)", len(calls), calls)
	}
	if calls[0].ID != "call_1" || calls[0].Name != "write_file" {
		t.Fatalf("tool call identity mismatch: %+v", calls[0])
	}
	if string(calls[0].Arguments) != `{"path":"a.txt"}` {
		t.Fatalf("tool call args mismatch: %q", string(calls[0].Arguments))
	}
}
