package openai

import (
	"context"
	"encoding/json"
	"errors"
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

func TestAdapter_Complete_MapsToResponsesAPI(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [
    {"type": "message", "content": [{"type":"output_text", "text":"Hello"}]}
  ],
  "usage": {"input_tokens": 1, "output_tokens": 2, "total_tokens": 3}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	reasoning := "low"
	resp, err := a.Complete(ctx, llm.Request{
		Model: "gpt-5.2",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
			llm.Assistant("a1"),
			llm.ToolResultNamed("call1", "shell", map[string]any{"ok": true}, false),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run shell",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
		ReasoningEffort: &reasoning,
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}

	// Assert request mapping.
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if gotBody["model"] != "gpt-5.2" {
		t.Fatalf("model: %v", gotBody["model"])
	}
	if instr, _ := gotBody["instructions"].(string); !strings.Contains(instr, "sys") || !strings.Contains(instr, "dev") {
		t.Fatalf("instructions: %q", instr)
	}
	if reasoningAny, ok := gotBody["reasoning"].(map[string]any); !ok || reasoningAny["effort"] != "low" {
		t.Fatalf("reasoning: %#v", gotBody["reasoning"])
	}
	if toolsAny, ok := gotBody["tools"].([]any); !ok || len(toolsAny) != 1 {
		t.Fatalf("tools: %#v", gotBody["tools"])
	}
	if inputAny, ok := gotBody["input"].([]any); !ok || len(inputAny) == 0 {
		t.Fatalf("input: %#v", gotBody["input"])
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
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	toolDef := llm.ToolDefinition{Name: "shell", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}

	cases := []struct {
		name string
		tc   *llm.ToolChoice
		want func(t *testing.T, v any)
	}{
		{
			name: "auto",
			tc:   &llm.ToolChoice{Mode: "auto"},
			want: func(t *testing.T, v any) {
				if v != "auto" {
					t.Fatalf("tool_choice: got %#v want %q", v, "auto")
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, v any) {
				if v != "none" {
					t.Fatalf("tool_choice: got %#v want %q", v, "none")
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, v any) {
				if v != "required" {
					t.Fatalf("tool_choice: got %#v want %q", v, "required")
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "shell"},
			want: func(t *testing.T, v any) {
				m, ok := v.(map[string]any)
				if !ok {
					t.Fatalf("tool_choice: %#v", v)
				}
				if m["type"] != "function" {
					t.Fatalf("tool_choice.type: %#v", m["type"])
				}
				fn, _ := m["function"].(map[string]any)
				if fn["name"] != "shell" {
					t.Fatalf("tool_choice.function.name: %#v", fn["name"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "gpt-5.2",
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
			tc.want(t, gotBody["tool_choice"])
		})
	}
}

func TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {
    "input_tokens": 1,
    "output_tokens": 2,
    "total_tokens": 3,
    "input_tokens_details": {"cached_tokens": 10},
    "output_tokens_details": {"reasoning_tokens": 7}
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if resp.Usage.CacheReadTokens == nil || *resp.Usage.CacheReadTokens != 10 {
		t.Fatalf("cache_read_tokens: %#v", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.ReasoningTokens == nil || *resp.Usage.ReasoningTokens != 7 {
		t.Fatalf("reasoning_tokens: %#v", resp.Usage.ReasoningTokens)
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
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
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
	params, _ := t0["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters.type: %#v", params["type"])
	}
}

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_Complete_HTTPErrorMapping_IncludesRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var rl *llm.RateLimitError
	if !errors.As(err, &rl) {
		t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
	}
	if rl.StatusCode() != 429 {
		t.Fatalf("status_code: %d", rl.StatusCode())
	}
	if rl.RetryAfter() == nil || *rl.RetryAfter() != 2*time.Second {
		t.Fatalf("retry_after: %v", rl.RetryAfter())
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
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

		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"Hel"}`)
		write("response.output_text.delta", `{"type":"response.output_text.delta","delta":"lo"}`)
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"message","content":[{"type":"output_text","text":"Hello"}]}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
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

	// Basic ordering check: STREAM_START before deltas; FINISH present.
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

func TestAdapter_Stream_TranslatesToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
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

		write("response.function_call_arguments.delta", `{"type":"response.function_call_arguments.delta","call_id":"call_1","name":"get_weather","delta":"{\"n\":1}"}`)
		write("response.output_item.done", `{"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}}`)
		write("response.completed", `{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2","output":[{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{\"n\":1}"}],"usage":{"input_tokens":1,"output_tokens":2,"total_tokens":3}}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	starts := 0
	deltas := 0
	ends := 0
	var startID, endID, name string
	var endArgs string
	var finishResp *llm.Response

	for ev := range stream.Events() {
		switch ev.Type {
		case llm.StreamEventToolCallStart:
			starts++
			if ev.ToolCall != nil {
				startID = ev.ToolCall.ID
				name = ev.ToolCall.Name
			}
		case llm.StreamEventToolCallDelta:
			deltas++
		case llm.StreamEventToolCallEnd:
			ends++
			if ev.ToolCall != nil {
				endID = ev.ToolCall.ID
				if name == "" {
					name = ev.ToolCall.Name
				}
				endArgs = string(ev.ToolCall.Arguments)
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}

	if starts != 1 || deltas < 1 || ends != 1 {
		t.Fatalf("tool call events: got starts=%d deltas=%d ends=%d", starts, deltas, ends)
	}
	if startID != "call_1" || endID != "call_1" {
		t.Fatalf("call ids: start=%q end=%q", startID, endID)
	}
	if name != "get_weather" {
		t.Fatalf("tool name: %q", name)
	}
	if strings.TrimSpace(endArgs) != `{"n":1}` {
		t.Fatalf("tool args: %q", endArgs)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	calls := finishResp.ToolCalls()
	if len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "get_weather" {
		t.Fatalf("finish tool calls: %+v", calls)
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
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
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
	if _, err := a.Complete(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	inputAny, ok := gotBody["input"].([]any)
	if !ok || len(inputAny) == 0 {
		t.Fatalf("input: %#v", gotBody["input"])
	}
	// Find first message item and inspect content.
	var content []any
	for _, it := range inputAny {
		m, ok := it.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "message" && m["role"] == "user" {
			if c, ok := m["content"].([]any); ok {
				content = c
			}
		}
	}
	if len(content) == 0 {
		t.Fatalf("missing message content in input: %#v", inputAny)
	}

	seenURL := false
	seenData := false
	seenFile := false
	for _, cAny := range content {
		c, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		if c["type"] != "input_image" {
			continue
		}
		u, _ := c["image_url"].(string)
		switch {
		case strings.HasPrefix(u, "https://example.com/"):
			seenURL = true
		case strings.HasPrefix(u, "data:image/png;base64,"):
			// Covers both raw data and file-path expansion.
			if seenData {
				seenFile = true
			} else {
				seenData = true
			}
		}
	}
	if !seenURL || !seenData || !seenFile {
		t.Fatalf("expected url+data+file images; seenURL=%v seenData=%v seenFile=%v content=%#v", seenURL, seenData, seenFile, content)
	}
}

func TestAdapter_Complete_ResponseFormat_JSONSchema(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"{}"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
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
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
			Strict:     true,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	rf, ok := gotBody["response_format"].(map[string]any)
	if !ok || rf == nil {
		t.Fatalf("response_format: %#v", gotBody["response_format"])
	}
	if rf["type"] != "json_schema" {
		t.Fatalf("response_format.type: %#v", rf["type"])
	}
	if _, ok := rf["json_schema"].(map[string]any); !ok {
		t.Fatalf("response_format.json_schema: %#v", rf["json_schema"])
	}
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
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

	st, err := a.Stream(ctx, llm.Request{Model: "gpt-5.2", Messages: []llm.Message{llm.User("hi")}})
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

func TestAdapter_ProviderOptions_PassThrough(t *testing.T) {
	var gotBody map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "id": "resp_1",
  "model": "gpt-5.2",
  "output": [{"type": "message", "content": [{"type":"output_text", "text":"ok"}]}],
  "usage": {"input_tokens": 1, "output_tokens": 1, "total_tokens": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gpt-5.2",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"openai": map[string]any{
				"parallel_tool_calls": true,
			},
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got, _ := gotBody["parallel_tool_calls"].(bool); !got {
		t.Fatalf("parallel_tool_calls: %#v", gotBody["parallel_tool_calls"])
	}
}
