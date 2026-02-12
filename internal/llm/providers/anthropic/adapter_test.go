package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/strongdm/kilroy/internal/llm"
)

func TestAdapter_Complete_MapsToMessagesAPI_AndSetsBetaHeaders(t *testing.T) {
	var gotBody map[string]any
	gotBeta := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBeta = r.Header.Get("anthropic-beta")
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"Hello"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "claude-test",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
			llm.Assistant("a1"),
			llm.ToolResultNamed("call1", "shell", "ok", false),
		},
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{
				"beta_headers": "prompt-caching-2024-07-31",
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}
	if gotBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta header: %q", gotBeta)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	sysBlocks, ok := gotBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system blocks: %#v", gotBody["system"])
	}
	sb0, _ := sysBlocks[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(sb0["text"]), "sys") || !strings.Contains(fmt.Sprint(sb0["text"]), "dev") {
		t.Fatalf("system text: %#v", sb0["text"])
	}
	if cc, _ := sb0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Fatalf("expected cache_control on system block; got %#v", sb0["cache_control"])
	}
	if msgsAny, ok := gotBody["messages"].([]any); !ok || len(msgsAny) == 0 {
		t.Fatalf("messages: %#v", gotBody["messages"])
	}

	// Conversation prefix breakpoint should be set on the message immediately before the last user message.
	seenPrefixCC := false
	msgsAny, _ := gotBody["messages"].([]any)
	for _, mAny := range msgsAny {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] != "assistant" {
			continue
		}
		blocks, _ := m["content"].([]any)
		for _, bAny := range blocks {
			bm, ok := bAny.(map[string]any)
			if !ok {
				continue
			}
			if cc, ok := bm["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
				seenPrefixCC = true
			}
		}
	}
	if !seenPrefixCC {
		t.Fatalf("expected cache_control breakpoint on conversation prefix; messages=%#v", gotBody["messages"])
	}
}

func TestAdapter_Complete_NormalizesDotsTodashesInModelID(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotModel, _ = body["model"].(string)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-sonnet-4-5",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	for _, tc := range []struct {
		input string
		want  string
	}{
		{"claude-sonnet-4.5", "claude-sonnet-4-5"},
		{"claude-opus-4.6", "claude-opus-4-6"},
		{"claude-3.7-sonnet", "claude-3-7-sonnet"},
		{"claude-sonnet-4-5", "claude-sonnet-4-5"},             // already dashes
		{"claude-sonnet-4-5-20250929", "claude-sonnet-4-5-20250929"}, // already native format
	} {
		t.Run(tc.input, func(t *testing.T) {
			gotModel = ""
			_, err := a.Complete(ctx, llm.Request{
				Model:    tc.input,
				Messages: []llm.Message{llm.User("hi")},
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if gotModel != tc.want {
				t.Fatalf("model sent to API: got %q, want %q", gotModel, tc.want)
			}
		})
	}
}

func TestAdapter_Stream_NormalizesDotsTodashesinModelID(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotModel, _ = body["model"].(string)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\ndata: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
		write("content_block_start", `{"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"delta":{"type":"text_delta","text":"ok"}}`)
		write("content_block_stop", `{}`)
		write("message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{
		Model:    "claude-opus-4.6",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for range stream.Events() {
	}
	_ = stream.Close()
	if gotModel != "claude-opus-4-6" {
		t.Fatalf("stream model sent to API: got %q, want %q", gotModel, "claude-opus-4-6")
	}
}

func TestAdapter_Complete_HTTPErrorMapping_AuthenticationError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ae *llm.AuthenticationError
	if !errors.As(err, &ae) {
		t.Fatalf("expected AuthenticationError, got %T (%v)", err, err)
	}
	if ae.StatusCode() != 401 {
		t.Fatalf("status_code: %d", ae.StatusCode())
	}
	if ae.Retryable() {
		t.Fatalf("expected non-retryable auth error")
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event string, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("content_block_start", `{"content_block":{"type":"text"}}`)
		write("content_block_delta", `{"delta":{"type":"text_delta","text":"Hel"}}`)
		write("content_block_delta", `{"delta":{"type":"text_delta","text":"lo"}}`)
		write("content_block_stop", `{}`)
		write("message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var deltas []string
	var kinds []llm.StreamEventType
	var finish *llm.Response
	for ev := range stream.Events() {
		kinds = append(kinds, ev.Type)
		if ev.Type == llm.StreamEventTextDelta {
			deltas = append(deltas, ev.Delta)
		}
		if ev.Type == llm.StreamEventFinish && ev.Response != nil {
			finish = ev.Response
		}
	}
	if strings.Join(deltas, "") != "Hello" {
		t.Fatalf("deltas: %q", strings.Join(deltas, ""))
	}
	if finish == nil || strings.TrimSpace(finish.Text()) != "Hello" {
		t.Fatalf("finish response: %+v", finish)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if v, _ := gotBody["stream"].(bool); !v {
		t.Fatalf("expected stream=true in request body; got %#v", gotBody["stream"])
	}
	if len(kinds) == 0 || kinds[0] != llm.StreamEventStreamStart {
		t.Fatalf("first event: got %v want %v (kinds=%v)", kinds, llm.StreamEventStreamStart, kinds)
	}
	foundTextStart := false
	foundTextEnd := false
	foundFinish := false
	for _, k := range kinds {
		if k == llm.StreamEventTextStart {
			foundTextStart = true
		}
		if k == llm.StreamEventTextEnd {
			foundTextEnd = true
		}
		if k == llm.StreamEventFinish {
			foundFinish = true
		}
	}
	if !foundTextStart || !foundTextEnd {
		t.Fatalf("expected TEXT_START and TEXT_END events (kinds=%v)", kinds)
	}
	if !foundFinish {
		t.Fatalf("expected FINISH event (kinds=%v)", kinds)
	}
}

func TestAdapter_Stream_TranslatesToolUseAndThinkingBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(event string, data string) {
			_, _ = io.WriteString(w, "event: "+event+"\n")
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write("content_block_start", `{"index":0,"content_block":{"type":"thinking","signature":"sig1"}}`)
		write("content_block_delta", `{"index":0,"delta":{"type":"thinking_delta","thinking":"Plan"}}`)
		write("content_block_stop", `{"index":0}`)

		write("content_block_start", `{"index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"get_weather"}}`)
		write("content_block_delta", `{"index":1,"delta":{"type":"input_json_delta","partial_json":"{\"n\":1}"}}`)
		write("content_block_stop", `{"index":1}`)

		write("message_delta", `{"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":2}}`)
		write("message_stop", `{}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	seenReasonStart := false
	seenReasonDelta := false
	seenReasonEnd := false
	seenToolStart := false
	seenToolDelta := false
	seenToolEnd := false
	var toolEnd llm.ToolCallData
	var finishResp *llm.Response

	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventReasoningStart:
			seenReasonStart = true
		case llm.StreamEventReasoningDelta:
			if strings.TrimSpace(ev.ReasoningDelta) != "" {
				seenReasonDelta = true
			}
		case llm.StreamEventReasoningEnd:
			seenReasonEnd = true
		case llm.StreamEventToolCallStart:
			seenToolStart = true
		case llm.StreamEventToolCallDelta:
			seenToolDelta = true
		case llm.StreamEventToolCallEnd:
			seenToolEnd = true
			if ev.ToolCall != nil {
				toolEnd = *ev.ToolCall
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}

	if !seenReasonStart || !seenReasonDelta || !seenReasonEnd {
		t.Fatalf("reasoning events: start=%t delta=%t end=%t", seenReasonStart, seenReasonDelta, seenReasonEnd)
	}
	if !seenToolStart || !seenToolDelta || !seenToolEnd {
		t.Fatalf("tool call events: start=%t delta=%t end=%t", seenToolStart, seenToolDelta, seenToolEnd)
	}
	if toolEnd.ID != "toolu_1" || toolEnd.Name != "get_weather" {
		t.Fatalf("tool call end: %+v", toolEnd)
	}
	if strings.TrimSpace(string(toolEnd.Arguments)) != `{"n":1}` {
		t.Fatalf("tool call args: %q", string(toolEnd.Arguments))
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	foundThinking := false
	foundTool := false
	for _, p := range finishResp.Message.Content {
		if p.Kind == llm.ContentThinking && p.Thinking != nil {
			if strings.TrimSpace(p.Thinking.Text) == "Plan" && strings.TrimSpace(p.Thinking.Signature) == "sig1" {
				foundThinking = true
			}
		}
		if p.Kind == llm.ContentToolCall && p.ToolCall != nil {
			if p.ToolCall.ID == "toolu_1" && p.ToolCall.Name == "get_weather" {
				foundTool = true
			}
		}
	}
	if !foundThinking {
		t.Fatalf("expected thinking content part in finish response: %+v", finishResp.Message.Content)
	}
	if !foundTool {
		t.Fatalf("expected tool call content part in finish response: %+v", finishResp.Message.Content)
	}
}

func TestAdapter_Stream_ToolUse_StartInputPlusDelta_NoDuplicateJSON(t *testing.T) {
	streamEvents := runKimiToolCallSequence(t, "start_input_plus_delta_duplicate")

	var startIDs []string
	var deltaIDs []string
	var endCall *llm.ToolCallData
	var finish *llm.Response

	for _, ev := range streamEvents {
		switch ev.Type {
		case llm.StreamEventError:
			if ev.Err != nil {
				t.Fatalf("stream error: %v", ev.Err)
			}
		case llm.StreamEventToolCallStart:
			if ev.ToolCall == nil {
				t.Fatalf("tool call start missing tool call payload")
			}
			startIDs = append(startIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallDelta:
			if ev.ToolCall == nil {
				t.Fatalf("tool call delta missing tool call payload")
			}
			deltaIDs = append(deltaIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall == nil {
				t.Fatalf("tool call end missing tool call payload")
			}
			tc := *ev.ToolCall
			endCall = &tc
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finish = ev.Response
			}
		}
	}

	if len(startIDs) != 1 {
		t.Fatalf("expected exactly one tool call start, got %d", len(startIDs))
	}
	if len(deltaIDs) == 0 {
		t.Fatalf("expected at least one tool call delta")
	}
	for i, id := range deltaIDs {
		if id != startIDs[0] {
			t.Fatalf("delta[%d] tool_call_id mismatch: got %q want %q", i, id, startIDs[0])
		}
	}
	if endCall == nil {
		t.Fatalf("expected tool call end event")
	}
	if endCall.ID != startIDs[0] {
		t.Fatalf("tool call end id mismatch: got %q want %q", endCall.ID, startIDs[0])
	}
	if finish == nil {
		t.Fatalf("expected finish response")
	}
	if finish.Finish.Reason != "tool_calls" {
		t.Fatalf("finish reason: got %q want %q", finish.Finish.Reason, "tool_calls")
	}

	if !json.Valid(endCall.Arguments) {
		t.Fatalf("tool call arguments must be valid JSON: %q", string(endCall.Arguments))
	}
	var args map[string]any
	if err := json.Unmarshal(endCall.Arguments, &args); err != nil {
		t.Fatalf("unmarshal tool args: %v", err)
	}
	if got := fmt.Sprint(args["command"]); got != "rg --files demo/rogue/original-rogue/*.c" {
		t.Fatalf("tool command: got %q", got)
	}

	calls := finish.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("finish response tool calls: got %d want 1", len(calls))
	}
	if calls[0].ID != startIDs[0] {
		t.Fatalf("finish tool call id mismatch: got %q want %q", calls[0].ID, startIDs[0])
	}
	if !json.Valid(calls[0].Arguments) {
		t.Fatalf("finish tool args must be valid JSON: %q", string(calls[0].Arguments))
	}
}

func TestAdapter_Stream_ToolUse_DeltaOnly_ValidJSON(t *testing.T) {
	streamEvents := runKimiToolCallSequence(t, "delta_only_valid_json")

	var startIDs []string
	var deltaIDs []string
	var endCall *llm.ToolCallData
	var finish *llm.Response

	for _, ev := range streamEvents {
		switch ev.Type {
		case llm.StreamEventError:
			if ev.Err != nil {
				t.Fatalf("stream error: %v", ev.Err)
			}
		case llm.StreamEventToolCallStart:
			if ev.ToolCall == nil {
				t.Fatalf("tool call start missing tool call payload")
			}
			startIDs = append(startIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallDelta:
			if ev.ToolCall == nil {
				t.Fatalf("tool call delta missing tool call payload")
			}
			deltaIDs = append(deltaIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall == nil {
				t.Fatalf("tool call end missing tool call payload")
			}
			tc := *ev.ToolCall
			endCall = &tc
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finish = ev.Response
			}
		}
	}

	if len(startIDs) != 1 {
		t.Fatalf("expected exactly one tool call start, got %d", len(startIDs))
	}
	if len(deltaIDs) < 2 {
		t.Fatalf("expected multiple tool call deltas, got %d", len(deltaIDs))
	}
	for i, id := range deltaIDs {
		if id != startIDs[0] {
			t.Fatalf("delta[%d] tool_call_id mismatch: got %q want %q", i, id, startIDs[0])
		}
	}
	if endCall == nil {
		t.Fatalf("expected tool call end event")
	}
	if endCall.ID != startIDs[0] {
		t.Fatalf("tool call end id mismatch: got %q want %q", endCall.ID, startIDs[0])
	}
	if !json.Valid(endCall.Arguments) {
		t.Fatalf("tool call arguments must be valid JSON: %q", string(endCall.Arguments))
	}
	var args map[string]any
	if err := json.Unmarshal(endCall.Arguments, &args); err != nil {
		t.Fatalf("unmarshal tool args: %v", err)
	}
	if got := fmt.Sprint(args["pattern"]); got != "*.c" {
		t.Fatalf("tool pattern: got %q want %q", got, "*.c")
	}
	if got := fmt.Sprint(args["path"]); got != "demo/rogue/original-rogue" {
		t.Fatalf("tool path: got %q", got)
	}
	if finish == nil {
		t.Fatalf("expected finish response")
	}
	if finish.Finish.Reason != "tool_calls" {
		t.Fatalf("finish reason: got %q want %q", finish.Finish.Reason, "tool_calls")
	}

	calls := finish.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("finish response tool calls: got %d want 1", len(calls))
	}
	if calls[0].ID != startIDs[0] {
		t.Fatalf("finish tool call id mismatch: got %q want %q", calls[0].ID, startIDs[0])
	}
	if !json.Valid(calls[0].Arguments) {
		t.Fatalf("finish tool args must be valid JSON: %q", string(calls[0].Arguments))
	}
}

func TestAdapter_Stream_ToolUse_OneOffBehaviorMatrix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name               string
		sequence           string
		wantJSONValid      bool
		wantToolName       string
		wantArgsContains   string
		wantParseErrSubstr string
	}{
		{
			name:             "start_input_only_is_valid",
			sequence:         "start_input_only_valid_json",
			wantJSONValid:    true,
			wantToolName:     "shell",
			wantArgsContains: `"command":"ls demo/rogue/original-rogue"`,
		},
		{
			name:             "delta_only_split_is_valid",
			sequence:         "delta_only_valid_json",
			wantJSONValid:    true,
			wantToolName:     "glob",
			wantArgsContains: `"pattern":"*.c"`,
		},
		{
			name:             "start_input_null_plus_delta_is_valid",
			sequence:         "start_input_null_plus_delta_valid_json",
			wantJSONValid:    true,
			wantToolName:     "shell",
			wantArgsContains: `"command":"echo ok"`,
		},
		{
			name:             "start_input_plus_full_delta_prefers_delta_and_is_valid",
			sequence:         "start_input_plus_delta_duplicate",
			wantJSONValid:    true,
			wantToolName:     "shell",
			wantArgsContains: `"command":"rg --files demo/rogue/original-rogue/*.c"`,
		},
		{
			name:               "start_input_plus_continuation_delta_is_invalid",
			sequence:           "start_input_plus_delta_continuation_invalid_json",
			wantJSONValid:      false,
			wantToolName:       "glob",
			wantArgsContains:   `,"path":"demo/rogue/original-rogue"}`,
			wantParseErrSubstr: `invalid character ','`,
		},
		{
			name:             "start_input_empty_object_plus_delta_prefers_delta_and_is_valid",
			sequence:         "start_input_empty_object_plus_delta_valid_json",
			wantJSONValid:    true,
			wantToolName:     "shell",
			wantArgsContains: `{"command":"echo hi"}`,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			streamEvents := runKimiToolCallSequence(t, tc.sequence)
			obs := observeSingleToolCall(t, streamEvents)
			if obs.finish.Finish.Reason != "tool_calls" {
				t.Fatalf("finish reason: got %q want %q", obs.finish.Finish.Reason, "tool_calls")
			}
			if obs.endCall.Name != tc.wantToolName {
				t.Fatalf("tool name: got %q want %q", obs.endCall.Name, tc.wantToolName)
			}
			if tc.wantArgsContains != "" && !strings.Contains(string(obs.endCall.Arguments), tc.wantArgsContains) {
				t.Fatalf("tool args %q do not contain %q", string(obs.endCall.Arguments), tc.wantArgsContains)
			}

			gotValid := json.Valid(obs.endCall.Arguments)
			if gotValid != tc.wantJSONValid {
				t.Fatalf("json validity mismatch: got %t want %t args=%q", gotValid, tc.wantJSONValid, string(obs.endCall.Arguments))
			}
			if !tc.wantJSONValid {
				var m map[string]any
				err := json.Unmarshal(obs.endCall.Arguments, &m)
				if err == nil {
					t.Fatalf("expected JSON parse error for args=%q", string(obs.endCall.Arguments))
				}
				if tc.wantParseErrSubstr != "" && !strings.Contains(err.Error(), tc.wantParseErrSubstr) {
					t.Fatalf("parse error mismatch: got %q want substring %q", err.Error(), tc.wantParseErrSubstr)
				}
				t.Logf("sequence=%s invalid_json_parse_error=%q args=%q", tc.sequence, err.Error(), string(obs.endCall.Arguments))
				return
			}

			var args map[string]any
			if err := json.Unmarshal(obs.endCall.Arguments, &args); err != nil {
				t.Fatalf("unmarshal tool args: %v", err)
			}
			t.Logf("sequence=%s valid_json=true keys=%v args=%q", tc.sequence, mapKeys(args), string(obs.endCall.Arguments))
		})
	}
}

func TestAdapter_Complete_ImageInput_URL_Data_AndFilePath(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	imgPath := filepath.Join(dir, "img.png")
	_ = os.WriteFile(imgPath, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msg := llm.Message{
		Role: llm.RoleUser,
		Content: []llm.ContentPart{
			{Kind: llm.ContentText, Text: "see"},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/x.png"}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01, 0x02, 0x03}}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath}},
		},
	}
	if _, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	msgs, ok := gotBody["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("messages: %#v", gotBody["messages"])
	}
	first, _ := msgs[0].(map[string]any)
	content, _ := first["content"].([]any)
	if len(content) == 0 {
		t.Fatalf("content: %#v", first["content"])
	}

	seenURL := false
	seenBase64 := 0
	for _, bAny := range content {
		bm, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		if bm["type"] != "image" {
			continue
		}
		src, _ := bm["source"].(map[string]any)
		st, _ := src["type"].(string)
		switch st {
		case "url":
			if src["url"] == "https://example.com/x.png" {
				seenURL = true
			}
		case "base64":
			if strings.TrimSpace(fmt.Sprint(src["data"])) != "" {
				seenBase64++
			}
		}
	}
	if !seenURL || seenBase64 < 2 {
		t.Fatalf("expected url image + 2 base64 images; seenURL=%v seenBase64=%d content=%#v", seenURL, seenBase64, content)
	}
}

func TestAdapter_ThinkingBlocks_RoundTripIncludingRedacted(t *testing.T) {
	var gotBodies []map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		var body map[string]any
		_ = json.Unmarshal(b, &body)
		gotBodies = append(gotBodies, body)

		w.Header().Set("Content-Type", "application/json")
		if len(gotBodies) == 1 {
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [
    {"type":"thinking","thinking":"THINK","signature":"sig1"},
    {"type":"redacted_thinking","data":"opaque"},
    {"type":"text","text":"Hello"}
  ],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 2}
}`))
			return
		}
		_, _ = w.Write([]byte(`{
  "id": "msg_2",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp1, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete 1: %v", err)
	}
	if got := len(resp1.Message.Content); got < 2 {
		t.Fatalf("expected thinking parts in response; got %+v", resp1.Message.Content)
	}

	_, err = a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi"), resp1.Message, llm.User("next")}})
	if err != nil {
		t.Fatalf("Complete 2: %v", err)
	}
	if len(gotBodies) < 2 {
		t.Fatalf("expected 2 captured bodies, got %d", len(gotBodies))
	}
	msgs, _ := gotBodies[1]["messages"].([]any)
	// Find assistant message blocks.
	var blocks []any
	for _, mAny := range msgs {
		m, ok := mAny.(map[string]any)
		if !ok {
			continue
		}
		if m["role"] == "assistant" {
			blocks, _ = m["content"].([]any)
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("expected assistant message with content blocks; messages=%#v", msgs)
	}

	seenThinking := false
	seenRedacted := false
	for _, bAny := range blocks {
		bm, ok := bAny.(map[string]any)
		if !ok {
			continue
		}
		switch bm["type"] {
		case "thinking":
			if bm["thinking"] == "THINK" && bm["signature"] == "sig1" {
				seenThinking = true
			}
		case "redacted_thinking":
			if bm["data"] == "opaque" {
				seenRedacted = true
			}
		}
	}
	if !seenThinking || !seenRedacted {
		t.Fatalf("expected thinking + redacted_thinking blocks; seenThinking=%v seenRedacted=%v blocks=%#v", seenThinking, seenRedacted, blocks)
	}
}

func TestAdapter_Complete_ResponseFormat_JSONSchema_InjectedIntoSystem(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"{}"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{"type": "string"},
		},
		"required": []string{"name"},
	}
	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	sysBlocks, ok := gotBody["system"].([]any)
	if !ok || len(sysBlocks) == 0 {
		t.Fatalf("system blocks: %#v", gotBody["system"])
	}
	sb0, _ := sysBlocks[0].(map[string]any)
	if !strings.Contains(fmt.Sprint(sb0["text"]), "JSON Schema") {
		t.Fatalf("expected schema instructions in system; got %#v", sb0["text"])
	}
}

func TestAdapter_ProviderOptions_PassThrough(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"anthropic": map[string]any{
				"x-test-opt": 123,
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, want := gotBody["x-test-opt"], float64(123); got != want {
		t.Fatalf("x-test-opt: got %#v want %#v", got, want)
	}
}

func TestAdapter_Complete_ToolChoice_MappedPerSpec(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	toolDef := llm.ToolDefinition{Name: "t1", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}

	cases := []struct {
		name string
		tc   *llm.ToolChoice
		want func(t *testing.T, body map[string]any)
	}{
		{
			name: "auto",
			tc:   &llm.ToolChoice{Mode: "auto"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "auto" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "any" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "t1"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; !ok {
					t.Fatalf("expected tools present")
				}
				tcAny, ok := body["tool_choice"].(map[string]any)
				if !ok || tcAny["type"] != "tool" || tcAny["name"] != "t1" {
					t.Fatalf("tool_choice: %#v", body["tool_choice"])
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, body map[string]any) {
				if _, ok := body["tools"]; ok {
					t.Fatalf("expected tools omitted for none; got %#v", body["tools"])
				}
				if _, ok := body["tool_choice"]; ok {
					t.Fatalf("expected tool_choice omitted for none; got %#v", body["tool_choice"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "claude-test",
				Messages:   []llm.Message{llm.User("hi")},
				Tools:      []llm.ToolDefinition{toolDef},
				ToolChoice: tc.tc,
			})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if gotBody == nil {
				t.Fatalf("server did not capture request body")
			}
			tc.want(t, gotBody)
		})
	}
}

func TestAdapter_Complete_ToolParameters_DefaultToEmptyObjectSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
		Tools:    []llm.ToolDefinition{{Name: "t1"}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	t0, _ := tools[0].(map[string]any)
	schema, _ := t0["input_schema"].(map[string]any)
	if schema["type"] != "object" {
		t.Fatalf("input_schema.type: %#v", schema["type"])
	}
}

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_PromptCaching_AutoCacheDefaultAndDisable(t *testing.T) {
	t.Run("default_enabled_injects_cache_control_and_beta", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "claude-test",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: []llm.ToolDefinition{
				{
					Name:        "t1",
					Description: "d",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if gotBeta != "prompt-caching-2024-07-31" {
			t.Fatalf("anthropic-beta: got %q want %q", gotBeta, "prompt-caching-2024-07-31")
		}

		sysBlocks, ok := gotBody["system"].([]any)
		if !ok || len(sysBlocks) == 0 {
			t.Fatalf("system blocks: %#v", gotBody["system"])
		}
		sb0, _ := sysBlocks[0].(map[string]any)
		if cc, _ := sb0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
			t.Fatalf("expected cache_control on system block; got %#v", sb0["cache_control"])
		}

		toolsAny, ok := gotBody["tools"].([]any)
		if !ok || len(toolsAny) != 1 {
			t.Fatalf("tools: %#v", gotBody["tools"])
		}
		t0, _ := toolsAny[0].(map[string]any)
		if cc, _ := t0["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
			t.Fatalf("expected cache_control on tool def; got %#v", t0["cache_control"])
		}

		// Breakpoint on conversation prefix (message before last user message: assistant "a1").
		msgs, ok := gotBody["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Fatalf("messages: %#v", gotBody["messages"])
		}
		seenPrefixCC := false
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			if m["role"] != "assistant" {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if cc, ok := bm["cache_control"].(map[string]any); ok && cc["type"] == "ephemeral" {
					seenPrefixCC = true
				}
			}
		}
		if !seenPrefixCC {
			t.Fatalf("expected cache_control breakpoint on conversation prefix; messages=%#v", gotBody["messages"])
		}
	})

	t.Run("large_toolset_uses_sparse_cache_control_breakpoints", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
		}))
		t.Cleanup(srv.Close)

		tools := make([]llm.ToolDefinition, 0, 10)
		for i := 0; i < 10; i++ {
			tools = append(tools, llm.ToolDefinition{
				Name:        fmt.Sprintf("t%d", i+1),
				Description: "d",
				Parameters: map[string]any{
					"type": "object",
				},
			})
		}

		a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "claude-test",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: tools,
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}
		if gotBeta != "prompt-caching-2024-07-31" {
			t.Fatalf("anthropic-beta: got %q want %q", gotBeta, "prompt-caching-2024-07-31")
		}

		totalCC := 0
		sysBlocks, ok := gotBody["system"].([]any)
		if !ok || len(sysBlocks) == 0 {
			t.Fatalf("system blocks: %#v", gotBody["system"])
		}
		if sb0, _ := sysBlocks[0].(map[string]any); sb0 != nil {
			if cc, _ := sb0["cache_control"].(map[string]any); cc["type"] == "ephemeral" {
				totalCC++
			}
		}

		toolCC := 0
		toolsAny, ok := gotBody["tools"].([]any)
		if !ok || len(toolsAny) != 10 {
			t.Fatalf("tools: %#v", gotBody["tools"])
		}
		for _, tAny := range toolsAny {
			tm, _ := tAny.(map[string]any)
			if tm == nil {
				continue
			}
			if cc, _ := tm["cache_control"].(map[string]any); cc["type"] == "ephemeral" {
				toolCC++
				totalCC++
			}
		}
		if toolCC != 1 {
			t.Fatalf("expected exactly 1 tool cache_control breakpoint, got %d", toolCC)
		}

		msgs, ok := gotBody["messages"].([]any)
		if !ok || len(msgs) == 0 {
			t.Fatalf("messages: %#v", gotBody["messages"])
		}
		for _, mAny := range msgs {
			m, _ := mAny.(map[string]any)
			if m == nil {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, _ := bAny.(map[string]any)
				if bm == nil {
					continue
				}
				if cc, _ := bm["cache_control"].(map[string]any); cc["type"] == "ephemeral" {
					totalCC++
				}
			}
		}
		if totalCC != 3 {
			t.Fatalf("expected 3 cache_control breakpoints (tools+system+prefix), got %d", totalCC)
		}
		if totalCC > 4 {
			t.Fatalf("expected <=4 cache_control breakpoints, got %d", totalCC)
		}
	})

	t.Run("disabled_does_not_inject_cache_control_or_beta", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "claude-test",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: []llm.ToolDefinition{
				{
					Name:        "t1",
					Description: "d",
					Parameters:  map[string]any{"type": "object"},
				},
			},
			ProviderOptions: map[string]any{
				"anthropic": map[string]any{
					"auto_cache":   false,
					"beta_headers": "x-test-beta",
				},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if gotBeta != "x-test-beta" {
			t.Fatalf("anthropic-beta: got %q want %q", gotBeta, "x-test-beta")
		}

		if _, ok := gotBody["system"].(string); !ok {
			t.Fatalf("expected system string when auto_cache=false; got %#v", gotBody["system"])
		}

		if toolsAny, ok := gotBody["tools"].([]any); ok && len(toolsAny) > 0 {
			t0, _ := toolsAny[0].(map[string]any)
			if _, ok := t0["cache_control"]; ok {
				t.Fatalf("unexpected cache_control on tool def when auto_cache=false: %#v", t0["cache_control"])
			}
		}

		msgs, _ := gotBody["messages"].([]any)
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := bm["cache_control"]; ok {
					t.Fatalf("unexpected cache_control in messages when auto_cache=false: %#v", bm["cache_control"])
				}
			}
		}
	})

	t.Run("non_anthropic_provider_default_does_not_inject_cache_control_or_beta", func(t *testing.T) {
		var gotBody map[string]any
		gotBeta := ""

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotBeta = r.Header.Get("anthropic-beta")
			b, _ := io.ReadAll(r.Body)
			_ = r.Body.Close()
			_ = json.Unmarshal(b, &gotBody)

			writeAnthropicStreamOK(w, "ok")
		}))
		t.Cleanup(srv.Close)

		a := &Adapter{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		_, err := a.Complete(ctx, llm.Request{
			Model: "kimi-k2.5",
			Messages: []llm.Message{
				llm.System("sys"),
				llm.User("u1"),
				llm.Assistant("a1"),
				llm.User("u2"),
			},
			Tools: []llm.ToolDefinition{
				{
					Name:        "t1",
					Description: "d",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		})
		if err != nil {
			t.Fatalf("Complete: %v", err)
		}

		if gotBeta != "" {
			t.Fatalf("anthropic-beta: got %q want empty", gotBeta)
		}
		if stream, _ := gotBody["stream"].(bool); !stream {
			t.Fatalf("expected stream=true for kimi, got %#v", gotBody["stream"])
		}
		if got := asInt(gotBody["max_tokens"]); got < 16000 {
			t.Fatalf("expected max_tokens>=16000 for kimi, got %d", got)
		}
		if _, ok := gotBody["system"].(string); !ok {
			t.Fatalf("expected system string when auto_cache defaults off for kimi; got %#v", gotBody["system"])
		}
		if toolsAny, ok := gotBody["tools"].([]any); ok && len(toolsAny) > 0 {
			t0, _ := toolsAny[0].(map[string]any)
			if _, ok := t0["cache_control"]; ok {
				t.Fatalf("unexpected cache_control on tool def: %#v", t0["cache_control"])
			}
		}
		msgs, _ := gotBody["messages"].([]any)
		for _, mAny := range msgs {
			m, ok := mAny.(map[string]any)
			if !ok {
				continue
			}
			blocks, _ := m["content"].([]any)
			for _, bAny := range blocks {
				bm, ok := bAny.(map[string]any)
				if !ok {
					continue
				}
				if _, ok := bm["cache_control"]; ok {
					t.Fatalf("unexpected cache_control in messages: %#v", bm["cache_control"])
				}
			}
		}
	})
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	st, err := a.Stream(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer st.Close()

	var sawErr error
	for ev := range st.Events() {
		if ev.Type == llm.StreamEventError && ev.Err != nil {
			sawErr = ev.Err
		}
	}
	if sawErr == nil {
		t.Fatalf("expected stream error")
	}
	var rte *llm.RequestTimeoutError
	if !errors.As(sawErr, &rte) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", sawErr, sawErr)
	}
}

func TestAdapter_UsageCacheTokens_Mapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "cache_read_input_tokens": 30,
    "cache_creation_input_tokens": 20
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 30 {
		t.Fatalf("cache_read_tokens: %#v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens == nil || *resp.Usage.CacheWriteTokens != 20 {
		t.Fatalf("cache_write_tokens: %#v", resp.Usage.CacheWriteTokens)
	}
}

func TestAdapter_Complete_DefaultMaxTokens_Is4096(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "claude-test",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	mt, ok := gotBody["max_tokens"].(float64)
	if !ok {
		t.Fatalf("max_tokens not found or not a number: %#v", gotBody["max_tokens"])
	}
	if int(mt) != 4096 {
		t.Fatalf("max_tokens: got %d want 4096", int(mt))
	}
}

func TestAdapter_Complete_FinishReason_Normalized(t *testing.T) {
	cases := []struct {
		name       string
		stopReason string
		wantReason string
		wantRaw    string
	}{
		{"end_turn", "end_turn", "stop", "end_turn"},
		{"stop_sequence", "stop_sequence", "stop", "stop_sequence"},
		{"max_tokens", "max_tokens", "length", "max_tokens"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": %q,
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`, tc.stopReason)
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
			if err != nil {
				t.Fatalf("Complete: %v", err)
			}
			if resp.Finish.Reason != tc.wantReason {
				t.Fatalf("Finish.Reason = %q, want %q", resp.Finish.Reason, tc.wantReason)
			}
			if resp.Finish.Raw != tc.wantRaw {
				t.Fatalf("Finish.Raw = %q, want %q", resp.Finish.Raw, tc.wantRaw)
			}
		})
	}
}

func TestAdapter_Complete_FinishReason_ToolUse_Normalized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "claude-test",
  "content": [{"type":"tool_use","id":"t1","name":"get_weather","input":{"n":1}}],
  "stop_reason": "tool_use",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "claude-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Finish.Reason != "tool_calls" {
		t.Fatalf("Finish.Reason = %q, want %q", resp.Finish.Reason, "tool_calls")
	}
	if resp.Finish.Raw != "tool_use" {
		t.Fatalf("Finish.Raw = %q, want %q", resp.Finish.Raw, "tool_use")
	}
}

func TestAdapter_KimiComplete_UsesStreamingTransportAndMinMaxTokens(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		stream, _ := gotBody["stream"].(bool)
		if !stream || asInt(gotBody["max_tokens"]) < 16000 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"stream=true and max_tokens>=16000 are required"}}`))
			return
		}
		writeAnthropicStreamOK(w, "ok")
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Provider: "kimi",
		Model:    "kimi-k2.5",
		Messages: []llm.Message{llm.User("hi")},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "ok" {
		t.Fatalf("response text=%q want %q", resp.Text(), "ok")
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if stream, _ := gotBody["stream"].(bool); !stream {
		t.Fatalf("expected stream=true for kimi complete path, got %#v", gotBody["stream"])
	}
	if got := asInt(gotBody["max_tokens"]); got < 16000 {
		t.Fatalf("expected max_tokens>=16000 for kimi complete path, got %d", got)
	}
}

func TestAdapter_KimiStream_EnforcesMinMaxTokens(t *testing.T) {
	tests := []struct {
		name      string
		maxTokens *int
	}{
		{name: "unset max tokens"},
		{name: "low max tokens", maxTokens: intPtr(16)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody map[string]any
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				b, _ := io.ReadAll(r.Body)
				_ = r.Body.Close()
				_ = json.Unmarshal(b, &gotBody)

				stream, _ := gotBody["stream"].(bool)
				if !stream || asInt(gotBody["max_tokens"]) < 16000 {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"stream=true and max_tokens>=16000 are required"}}`))
					return
				}
				writeAnthropicStreamOK(w, "ok")
			}))
			t.Cleanup(srv.Close)

			a := &Adapter{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()

			st, err := a.Stream(ctx, llm.Request{
				Provider:  "kimi",
				Model:     "kimi-k2.5",
				Messages:  []llm.Message{llm.User("hi")},
				MaxTokens: tc.maxTokens,
			})
			if err != nil {
				t.Fatalf("Stream: %v", err)
			}
			defer st.Close()

			var finish *llm.Response
			var streamErr error
			for ev := range st.Events() {
				if ev.Type == llm.StreamEventFinish && ev.Response != nil {
					finish = ev.Response
				}
				if ev.Type == llm.StreamEventError && ev.Err != nil {
					streamErr = ev.Err
				}
			}
			if streamErr != nil {
				t.Fatalf("stream error: %v", streamErr)
			}
			if finish == nil || strings.TrimSpace(finish.Text()) != "ok" {
				t.Fatalf("finish response=%+v want text %q", finish, "ok")
			}
			if gotBody == nil {
				t.Fatalf("server did not capture request body")
			}
			if stream, _ := gotBody["stream"].(bool); !stream {
				t.Fatalf("expected stream=true, got %#v", gotBody["stream"])
			}
			if got := asInt(gotBody["max_tokens"]); got < 16000 {
				t.Fatalf("expected max_tokens>=16000, got %d", got)
			}
		})
	}
}

func asInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	default:
		return 0
	}
}

func intPtr(v int) *int { return &v }

type streamFixtureEvent struct {
	Sequence string          `json:"sequence"`
	Event    string          `json:"event"`
	Data     json.RawMessage `json:"data"`
}

type toolCallObservation struct {
	startID string
	endCall llm.ToolCallData
	finish  *llm.Response
}

func observeSingleToolCall(t *testing.T, streamEvents []llm.StreamEvent) toolCallObservation {
	t.Helper()

	startIDs := make([]string, 0, 1)
	deltaIDs := make([]string, 0, 4)
	var endCall *llm.ToolCallData
	var finish *llm.Response
	for _, ev := range streamEvents {
		switch ev.Type {
		case llm.StreamEventError:
			if ev.Err != nil {
				t.Fatalf("stream error: %v", ev.Err)
			}
		case llm.StreamEventToolCallStart:
			if ev.ToolCall == nil {
				t.Fatalf("tool call start missing payload")
			}
			startIDs = append(startIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallDelta:
			if ev.ToolCall == nil {
				t.Fatalf("tool call delta missing payload")
			}
			deltaIDs = append(deltaIDs, ev.ToolCall.ID)
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall == nil {
				t.Fatalf("tool call end missing payload")
			}
			tc := *ev.ToolCall
			endCall = &tc
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finish = ev.Response
			}
		}
	}

	if len(startIDs) != 1 {
		t.Fatalf("expected one tool call start, got %d", len(startIDs))
	}
	for i, id := range deltaIDs {
		if id != startIDs[0] {
			t.Fatalf("delta[%d] tool_call_id mismatch: got %q want %q", i, id, startIDs[0])
		}
	}
	if endCall == nil {
		t.Fatalf("expected tool call end event")
	}
	if endCall.ID != startIDs[0] {
		t.Fatalf("tool call end id mismatch: got %q want %q", endCall.ID, startIDs[0])
	}
	if finish == nil {
		t.Fatalf("expected finish response")
	}
	calls := finish.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("finish response tool calls: got %d want 1", len(calls))
	}
	if calls[0].ID != startIDs[0] {
		t.Fatalf("finish tool call id mismatch: got %q want %q", calls[0].ID, startIDs[0])
	}
	if string(calls[0].Arguments) != string(endCall.Arguments) {
		t.Fatalf("finish/end tool args mismatch: finish=%q end=%q", string(calls[0].Arguments), string(endCall.Arguments))
	}

	return toolCallObservation{
		startID: startIDs[0],
		endCall: *endCall,
		finish:  finish,
	}
}

func runKimiToolCallSequence(t *testing.T, sequence string) []llm.StreamEvent {
	t.Helper()

	events := loadStreamFixtureSequence(t, sequence)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		for _, ev := range events {
			_, _ = io.WriteString(w, "event: "+ev.Event+"\n")
			_, _ = io.WriteString(w, "data: "+string(ev.Data)+"\n\n")
			if f != nil {
				f.Flush()
			}
		}
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{Provider: "kimi", APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Provider: "kimi", Model: "kimi-k2.5", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var out []llm.StreamEvent
	for ev := range stream.Events() {
		out = append(out, ev)
	}
	if len(out) == 0 {
		t.Fatalf("expected stream events")
	}
	return out
}

func loadStreamFixtureSequence(t *testing.T, sequence string) []streamFixtureEvent {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("testdata", "kimi_tool_call_sequences.ndjson"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	lines := strings.Split(string(b), "\n")
	out := make([]streamFixtureEvent, 0, len(lines))
	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		var ev streamFixtureEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse fixture line %d: %v", i+1, err)
		}
		if ev.Sequence != sequence {
			continue
		}
		if strings.TrimSpace(ev.Event) == "" {
			t.Fatalf("fixture line %d has empty event", i+1)
		}
		if len(ev.Data) == 0 {
			t.Fatalf("fixture line %d has empty data", i+1)
		}
		out = append(out, ev)
	}
	if len(out) == 0 {
		t.Fatalf("fixture sequence %q not found", sequence)
	}
	return out
}

func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeAnthropicStreamOK(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "text/event-stream")
	f, _ := w.(http.Flusher)
	write := func(event string, data string) {
		_, _ = io.WriteString(w, "event: "+event+"\n")
		_, _ = io.WriteString(w, "data: "+data+"\n\n")
		if f != nil {
			f.Flush()
		}
	}
	write("content_block_start", `{"content_block":{"type":"text"}}`)
	write("content_block_delta", fmt.Sprintf(`{"delta":{"type":"text_delta","text":%q}}`, text))
	write("content_block_stop", `{}`)
	write("message_delta", `{"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
	write("message_stop", `{}`)
}
