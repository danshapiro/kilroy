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

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
  "id": "msg_1",
  "model": "kimi-for-coding",
  "content": [{"type":"text","text":"ok"}],
  "stop_reason": "end_turn",
  "usage": {"input_tokens": 1, "output_tokens": 1}
}`))
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
