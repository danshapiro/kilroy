package codexappserver

import (
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/llm"
)

func testNotification(method string, params map[string]any) map[string]any {
	return map[string]any{"method": method, "params": params}
}

func TestTranslateResponse_ToolProtocolReasoningAndUsage(t *testing.T) {
	body := map[string]any{
		"id":    "resp_codex_1",
		"model": "codex-mini",
		"turn": map[string]any{
			"id":     "turn_1",
			"status": "completed",
		},
		"notifications": []any{
			testNotification("item/completed", map[string]any{
				"item": map[string]any{
					"id":      "reasoning_1",
					"type":    "reasoning",
					"summary": []any{"Plan steps"},
					"content": []any{"Visible [[REDACTED_REASONING]]secret[[/REDACTED_REASONING]] done"},
				},
			}),
			testNotification("item/completed", map[string]any{
				"item": map[string]any{
					"id":   "agent_1",
					"type": "agentMessage",
					"text": "Before [[TOOL_CALL]]{\"id\":\"call_1\",\"name\":\"search\",\"arguments\":{\"q\":\"foo\"}}[[/TOOL_CALL]] After",
				},
			}),
			testNotification("thread/tokenUsage/updated", map[string]any{
				"tokenUsage": map[string]any{
					"last": map[string]any{
						"inputTokens":           11,
						"outputTokens":          7,
						"totalTokens":           18,
						"reasoningOutputTokens": 3,
						"cachedInputTokens":     2,
					},
				},
			}),
		},
	}

	response, err := translateResponse(body)
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if response.ID != "turn_1" {
		t.Fatalf("response id: got %q want turn_1", response.ID)
	}
	if response.Model != "codex-mini" {
		t.Fatalf("response model: got %q", response.Model)
	}
	if response.Provider != providerName {
		t.Fatalf("response provider: got %q", response.Provider)
	}
	if response.Finish.Reason != llm.FinishReasonToolCalls {
		t.Fatalf("finish reason: got %q want %q", response.Finish.Reason, llm.FinishReasonToolCalls)
	}
	if response.Usage.InputTokens != 11 || response.Usage.OutputTokens != 7 || response.Usage.TotalTokens != 18 {
		t.Fatalf("usage mismatch: %+v", response.Usage)
	}
	if response.Usage.ReasoningTokens == nil || *response.Usage.ReasoningTokens != 3 {
		t.Fatalf("reasoning tokens mismatch: %+v", response.Usage)
	}
	if response.Usage.CacheReadTokens == nil || *response.Usage.CacheReadTokens != 2 {
		t.Fatalf("cache read tokens mismatch: %+v", response.Usage)
	}

	if len(response.Message.Content) < 4 {
		t.Fatalf("expected content parts, got %+v", response.Message.Content)
	}
	foundToolCall := false
	for _, part := range response.Message.Content {
		if part.Kind == llm.ContentToolCall && part.ToolCall != nil {
			foundToolCall = true
			if part.ToolCall.ID != "call_1" || part.ToolCall.Name != "search" {
				t.Fatalf("tool call mismatch: %+v", part.ToolCall)
			}
			if strings.TrimSpace(string(part.ToolCall.Arguments)) != `{"q":"foo"}` {
				t.Fatalf("tool arguments mismatch: %q", string(part.ToolCall.Arguments))
			}
		}
	}
	if !foundToolCall {
		t.Fatalf("expected tool call part in response content")
	}
}

func TestTranslateResponse_FinishReasonMapping(t *testing.T) {
	interrupted, err := translateResponse(map[string]any{
		"model": "codex-mini",
		"turn":  map[string]any{"id": "turn_2", "status": "interrupted"},
	})
	if err != nil {
		t.Fatalf("translateResponse interrupted: %v", err)
	}
	if interrupted.Finish.Reason != llm.FinishReasonLength {
		t.Fatalf("interrupted finish reason: got %q", interrupted.Finish.Reason)
	}

	failed, err := translateResponse(map[string]any{
		"model": "codex-mini",
		"turn":  map[string]any{"id": "turn_3", "status": "failed"},
	})
	if err != nil {
		t.Fatalf("translateResponse failed: %v", err)
	}
	if failed.Finish.Reason != llm.FinishReasonError {
		t.Fatalf("failed finish reason: got %q", failed.Finish.Reason)
	}
}

func TestTranslateResponse_ReconstructsFromDeltas(t *testing.T) {
	body := map[string]any{
		"model": "codex-mini",
		"turn":  map[string]any{"id": "turn_4", "status": "completed"},
		"notifications": []any{
			testNotification("item/agentMessage/delta", map[string]any{"itemId": "agent_delta", "delta": "Hello "}),
			testNotification("item/agentMessage/delta", map[string]any{"itemId": "agent_delta", "delta": "world"}),
			testNotification("item/reasoning/summaryTextDelta", map[string]any{"itemId": "reason_delta", "summaryIndex": 0, "delta": "Need to inspect state"}),
		},
	}

	response, err := translateResponse(body)
	if err != nil {
		t.Fatalf("translateResponse: %v", err)
	}
	if len(response.Message.Content) != 2 {
		t.Fatalf("content len: got %d want 2 (%+v)", len(response.Message.Content), response.Message.Content)
	}
	if response.Message.Content[0].Kind != llm.ContentText || response.Message.Content[0].Text != "Hello world" {
		t.Fatalf("agent text reconstruction mismatch: %+v", response.Message.Content[0])
	}
	if response.Message.Content[1].Kind != llm.ContentThinking || response.Message.Content[1].Thinking == nil || response.Message.Content[1].Thinking.Text != "Need to inspect state" {
		t.Fatalf("reasoning reconstruction mismatch: %+v", response.Message.Content[1])
	}
}
