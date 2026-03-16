package llm

import "testing"

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
