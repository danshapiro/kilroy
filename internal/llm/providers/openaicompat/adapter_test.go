package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

func assertRateLimitInfo(t *testing.T, rl *llm.RateLimitInfo) {
	t.Helper()
	if rl == nil {
		t.Fatalf("expected rate limit info, got nil")
	}
	if rl.RequestsRemaining == nil || *rl.RequestsRemaining != 9 {
		t.Fatalf("requests_remaining: %#v", rl.RequestsRemaining)
	}
	if rl.RequestsLimit == nil || *rl.RequestsLimit != 10 {
		t.Fatalf("requests_limit: %#v", rl.RequestsLimit)
	}
	if rl.TokensRemaining == nil || *rl.TokensRemaining != 90 {
		t.Fatalf("tokens_remaining: %#v", rl.TokensRemaining)
	}
	if rl.TokensLimit == nil || *rl.TokensLimit != 100 {
		t.Fatalf("tokens_limit: %#v", rl.TokensLimit)
	}
	if rl.ResetAt != "2025-01-01T00:00:10Z" {
		t.Fatalf("reset_at: %q", rl.ResetAt)
	}
}

func TestAdapter_Complete_ChatCompletionsMapsToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path: %s", r.URL.Path)
		}
		w.Header().Set("x-ratelimit-remaining-requests", "9")
		w.Header().Set("x-ratelimit-limit-requests", "10")
		w.Header().Set("x-ratelimit-remaining-tokens", "90")
		w.Header().Set("x-ratelimit-limit-tokens", "100")
		w.Header().Set("x-ratelimit-reset-requests", "Wed, 01 Jan 2025 00:00:10 GMT")
		_, _ = w.Write([]byte(`{"id":"c1","model":"m","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"file_path\":\"README.md\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`))
	}))
	defer srv.Close()

	a := NewAdapter(Config{
		Provider:   "kimi",
		APIKey:     "k",
		BaseURL:    srv.URL,
		Path:       "/v1/chat/completions",
		OptionsKey: "kimi",
	})
	resp, err := a.Complete(context.Background(), llm.Request{
		Provider: "kimi",
		Model:    "kimi-k2.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if len(resp.ToolCalls()) != 1 {
		t.Fatalf("tool call mapping failed")
	}
	assertRateLimitInfo(t, resp.RateLimit)
}

func TestAdapter_Stream_EmitsFinishEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("x-ratelimit-remaining-requests", "9")
		w.Header().Set("x-ratelimit-limit-requests", "10")
		w.Header().Set("x-ratelimit-remaining-tokens", "90")
		w.Header().Set("x-ratelimit-limit-tokens", "100")
		w.Header().Set("x-ratelimit-reset-requests", "Wed, 01 Jan 2025 00:00:10 GMT")
		_, _ = w.Write([]byte("data: {\"id\":\"c2\",\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"c2\",\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "zai", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "zai",
		Model:    "glm-4.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var finish *llm.Response
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventFinish {
			finish = ev.Response
			break
		}
	}
	if finish == nil {
		t.Fatalf("expected finish event")
	}
	assertRateLimitInfo(t, finish.RateLimit)
}

func TestAdapter_Stream_MapsToolCallDeltasToEventsAndFinalResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"c3\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"path\\\":\\\"REA\"}}]},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"id\":\"c3\",\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"DME.md\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":4,\"total_tokens\":6}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "zai", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "zai",
		Model:    "glm-4.7",
		Messages: []llm.Message{llm.User("hi")},
		Tools: []llm.ToolDefinition{{
			Name:       "read_file",
			Parameters: map[string]any{"type": "object"},
		}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var (
		sawStart   bool
		sawDelta   bool
		sawEnd     bool
		finishResp *llm.Response
	)
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventToolCallStart:
			sawStart = true
		case llm.StreamEventToolCallDelta:
			sawDelta = true
		case llm.StreamEventToolCallEnd:
			sawEnd = true
			if ev.ToolCall == nil || ev.ToolCall.ID != "call_1" {
				t.Fatalf("unexpected tool call end payload: %#v", ev.ToolCall)
			}
			if got := string(ev.ToolCall.Arguments); got != "{\"path\":\"README.md\"}" {
				t.Fatalf("tool args mismatch: %q", got)
			}
		case llm.StreamEventFinish:
			finishResp = ev.Response
		}
	}
	if !sawStart || !sawDelta || !sawEnd {
		t.Fatalf("expected tool start/delta/end events, got start=%v delta=%v end=%v", sawStart, sawDelta, sawEnd)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response payload")
	}
	calls := finishResp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("expected one final tool call, got %d", len(calls))
	}
	if calls[0].ID != "call_1" || calls[0].Name != "read_file" {
		t.Fatalf("unexpected final tool call: %#v", calls[0])
	}
	if got := string(calls[0].Arguments); got != "{\"path\":\"README.md\"}" {
		t.Fatalf("final tool args mismatch: %q", got)
	}
}

func TestAdapter_Stream_RequestBodyPreservesLargeIntegerOptions(t *testing.T) {
	const big = "9007199254740993"
	var seen map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		if err := dec.Decode(&seen); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, OptionsKey: "kimi"})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "kimi",
		Model:    "kimi-k2.5",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"kimi": map[string]any{"seed": json.Number(big)},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	stream.Close()

	if got, ok := seen["seed"].(json.Number); !ok || got.String() != big {
		t.Fatalf("seed mismatch: %#v", seen["seed"])
	}
}

func TestAdapter_Stream_ParsesMultiLineSSEData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":\"stop\"}],\n"))
		_, _ = w.Write([]byte("data: \"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":2,\"total_tokens\":3}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "zai", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "zai",
		Model:    "glm-4.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var text strings.Builder
	for ev := range stream.Events() {
		if ev.Type == llm.StreamEventTextDelta {
			text.WriteString(ev.Delta)
		}
	}
	if text.String() != "hello" {
		t.Fatalf("text delta mismatch: %q", text.String())
	}
}

func TestAdapter_Stream_UsageOnlyChunkPreservesTokenAccounting(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":7,\"total_tokens\":12}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "zai", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "zai",
		Model:    "glm-4.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var finishUsage llm.Usage
	sawFinish := false
	for ev := range stream.Events() {
		if ev.Type != llm.StreamEventFinish || ev.Usage == nil {
			continue
		}
		sawFinish = true
		finishUsage = *ev.Usage
	}
	if !sawFinish {
		t.Fatalf("expected finish event")
	}
	if finishUsage.TotalTokens != 12 {
		t.Fatalf("usage mismatch: %#v", finishUsage)
	}
}

func TestFromChatCompletions_ExtractsReasoningContentDeepSeek(t *testing.T) {
	raw := map[string]any{
		"id":    "r1",
		"model": "deepseek-r1",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"role":              "assistant",
				"content":           "The answer is 42.",
				"reasoning_content": "Let me think step by step...",
			},
		}},
		"usage": map[string]any{
			"prompt_tokens": json.Number("10"), "completion_tokens": json.Number("20"), "total_tokens": json.Number("30"),
		},
	}
	resp, err := fromChatCompletions("deepseek", "deepseek-r1", raw)
	if err != nil {
		t.Fatalf("fromChatCompletions: %v", err)
	}
	if got := resp.ReasoningText(); got != "Let me think step by step..." {
		t.Fatalf("reasoning text: got %q", got)
	}
	if got := resp.Message.Content[0].Kind; got != llm.ContentThinking {
		t.Fatalf("first content part: got %q want %q", got, llm.ContentThinking)
	}
	if got := resp.Message.Content[1].Text; got != "The answer is 42." {
		t.Fatalf("text content: got %q", got)
	}
}

func TestFromChatCompletions_ExtractsReasoningCerebras(t *testing.T) {
	raw := map[string]any{
		"id":    "r2",
		"model": "zai-glm-4.7",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message": map[string]any{
				"role":      "assistant",
				"content":   "Result here.",
				"reasoning": "Cerebras reasoning trace...",
			},
		}},
		"usage": map[string]any{
			"prompt_tokens": json.Number("5"), "completion_tokens": json.Number("15"), "total_tokens": json.Number("20"),
		},
	}
	resp, err := fromChatCompletions("cerebras", "zai-glm-4.7", raw)
	if err != nil {
		t.Fatalf("fromChatCompletions: %v", err)
	}
	if got := resp.ReasoningText(); got != "Cerebras reasoning trace..." {
		t.Fatalf("reasoning text: got %q", got)
	}
}

func TestFromChatCompletions_ExtractsReasoningTokensFromUsage(t *testing.T) {
	raw := map[string]any{
		"id":    "r3",
		"model": "zai-glm-4.7",
		"choices": []any{map[string]any{
			"finish_reason": "stop",
			"message":       map[string]any{"role": "assistant", "content": "ok"},
		}},
		"usage": map[string]any{
			"prompt_tokens": json.Number("10"), "completion_tokens": json.Number("50"), "total_tokens": json.Number("60"),
			"completion_tokens_details": map[string]any{
				"reasoning_tokens": json.Number("35"),
			},
		},
	}
	resp, err := fromChatCompletions("cerebras", "zai-glm-4.7", raw)
	if err != nil {
		t.Fatalf("fromChatCompletions: %v", err)
	}
	if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 35 {
		t.Fatalf("reasoning tokens: got %v", resp.Usage.ReasoningTokens)
	}
}

func TestToChatCompletionsBody_IncludesReasoningEffort(t *testing.T) {
	effort := "high"
	body, err := toChatCompletionsBody(llm.Request{
		Model:           "zai-glm-4.7",
		Messages:        []llm.Message{llm.User("hi")},
		ReasoningEffort: &effort,
	}, "cerebras", "cerebras", chatCompletionsBodyOptions{})
	if err != nil {
		t.Fatalf("toChatCompletionsBody: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got, ok := m["reasoning_effort"].(string); !ok || got != "high" {
		t.Fatalf("reasoning_effort: got %v", m["reasoning_effort"])
	}
}

func TestToChatCompletionsMessages_SkipsThinkingParts(t *testing.T) {
	msgs := []llm.Message{{
		Role: llm.RoleAssistant,
		Content: []llm.ContentPart{
			{Kind: llm.ContentThinking, Thinking: &llm.ThinkingData{Text: "internal reasoning"}},
			{Kind: llm.ContentText, Text: "visible reply"},
		},
	}}
	out := toChatCompletionsMessages(msgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 message, got %d", len(out))
	}
	if got := out[0]["content"].(string); got != "visible reply" {
		t.Fatalf("content: got %q", got)
	}
}

func TestAdapter_Stream_ReasoningDeltasDeepSeek(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"step 1\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\" step 2\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":10,\"total_tokens\":15}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "deepseek", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "deepseek",
		Model:    "deepseek-r1",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var (
		sawReasoningStart bool
		sawReasoningEnd   bool
		reasoningDeltas   strings.Builder
		finishResp        *llm.Response
	)
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventReasoningStart:
			sawReasoningStart = true
		case llm.StreamEventReasoningDelta:
			reasoningDeltas.WriteString(ev.ReasoningDelta)
		case llm.StreamEventReasoningEnd:
			sawReasoningEnd = true
		case llm.StreamEventFinish:
			finishResp = ev.Response
		}
	}
	if !sawReasoningStart {
		t.Fatalf("expected REASONING_START event")
	}
	if !sawReasoningEnd {
		t.Fatalf("expected REASONING_END event")
	}
	if got := reasoningDeltas.String(); got != "step 1 step 2" {
		t.Fatalf("reasoning deltas: got %q", got)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	if got := finishResp.ReasoningText(); got != "step 1 step 2" {
		t.Fatalf("final reasoning text: got %q", got)
	}
}

func TestAdapter_Stream_ReasoningDeltasCerebras(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"reasoning\":\"thinking...\"},\"finish_reason\":null}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":8,\"total_tokens\":11,\"completion_tokens_details\":{\"reasoning_tokens\":5}}}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer srv.Close()

	a := NewAdapter(Config{Provider: "cerebras", APIKey: "k", BaseURL: srv.URL})
	stream, err := a.Stream(context.Background(), llm.Request{
		Provider: "cerebras",
		Model:    "zai-glm-4.7",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var (
		sawReasoningStart bool
		reasoningText     strings.Builder
		finishUsage       *llm.Usage
	)
	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventReasoningStart:
			sawReasoningStart = true
		case llm.StreamEventReasoningDelta:
			reasoningText.WriteString(ev.ReasoningDelta)
		case llm.StreamEventFinish:
			finishUsage = ev.Usage
		}
	}
	if !sawReasoningStart {
		t.Fatalf("expected REASONING_START event")
	}
	if got := reasoningText.String(); got != "thinking..." {
		t.Fatalf("reasoning deltas: got %q", got)
	}
	if finishUsage == nil || finishUsage.ReasoningTokens == nil || *finishUsage.ReasoningTokens != 5 {
		t.Fatalf("expected reasoning_tokens=5 in usage, got %+v", finishUsage)
	}
}

func TestWithDefaultRequestDeadline_AddsDeadlineWhenMissing(t *testing.T) {
	ctx, cancel := withDefaultRequestDeadline(context.Background())
	defer cancel()

	if _, ok := ctx.Deadline(); !ok {
		t.Fatalf("expected derived context deadline")
	}
}

func TestWithDefaultRequestDeadline_PreservesExistingDeadline(t *testing.T) {
	origCtx, origCancel := context.WithTimeout(context.Background(), time.Hour)
	defer origCancel()
	origDeadline, _ := origCtx.Deadline()

	ctx, cancel := withDefaultRequestDeadline(origCtx)
	defer cancel()

	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatalf("expected deadline to remain present")
	}
	if !deadline.Equal(origDeadline) {
		t.Fatalf("deadline changed: got %v want %v", deadline, origDeadline)
	}
}
