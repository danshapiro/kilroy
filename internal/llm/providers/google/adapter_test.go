package google

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

func TestAdapter_Complete_MapsToGeminiGenerateContent(t *testing.T) {
	var gotBody map[string]any
	gotKey := ""
	gotPath := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":generateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"Hello"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 2, "totalTokenCount": 3}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{
		Model: "gemini-test",
		Messages: []llm.Message{
			llm.System("sys"),
			llm.Developer("dev"),
			llm.User("u1"),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "shell",
			Description: "run shell",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if strings.TrimSpace(resp.Text()) != "Hello" {
		t.Fatalf("resp text: %q", resp.Text())
	}
	if gotKey != "k" {
		t.Fatalf("key param: %q", gotKey)
	}
	if !strings.Contains(gotPath, "/v1beta/models/") {
		t.Fatalf("path: %q", gotPath)
	}

	// Request mapping basics.
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if _, ok := gotBody["contents"].([]any); !ok {
		t.Fatalf("contents: %#v", gotBody["contents"])
	}
	if sysAny, ok := gotBody["systemInstruction"].(map[string]any); !ok || sysAny == nil {
		t.Fatalf("systemInstruction: %#v", gotBody["systemInstruction"])
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
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
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "AUTO" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "none",
			tc:   &llm.ToolChoice{Mode: "none"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "NONE" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "required",
			tc:   &llm.ToolChoice{Mode: "required"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "ANY" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
			},
		},
		{
			name: "named",
			tc:   &llm.ToolChoice{Mode: "named", Name: "t1"},
			want: func(t *testing.T, body map[string]any) {
				tcAny, _ := body["toolConfig"].(map[string]any)
				fc, _ := tcAny["functionCallingConfig"].(map[string]any)
				if fc["mode"] != "ANY" {
					t.Fatalf("toolConfig.functionCallingConfig.mode: %#v", fc["mode"])
				}
				allow, _ := fc["allowedFunctionNames"].([]any)
				if len(allow) != 1 || allow[0] != "t1" {
					t.Fatalf("allowedFunctionNames: %#v", fc["allowedFunctionNames"])
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotBody = nil
			_, err := a.Complete(ctx, llm.Request{
				Model:      "gemini-test",
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

func TestAdapter_Complete_Usage_MapsReasoningAndCacheTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {
    "promptTokenCount": 1,
    "candidatesTokenCount": 2,
    "totalTokenCount": 3,
    "cachedContentTokenCount": 10,
    "thoughtsTokenCount": 7
  }
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
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
	fds, _ := t0["functionDeclarations"].([]any)
	if len(fds) != 1 {
		t.Fatalf("functionDeclarations: %#v", t0["functionDeclarations"])
	}
	fd0, _ := fds[0].(map[string]any)
	params, _ := fd0["parameters"].(map[string]any)
	if params["type"] != "object" {
		t.Fatalf("parameters.type: %#v", params["type"])
	}
}

func TestAdapter_Complete_RejectsAudioAndDocumentParts(t *testing.T) {
	a := &Adapter{APIKey: "k", BaseURL: "http://example.com"}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	msgAudio := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav"}}}}
	_, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msgAudio}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var ce *llm.ConfigurationError
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}

	msgDoc := llm.Message{Role: llm.RoleUser, Content: []llm.ContentPart{{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/a.pdf"}}}}
	_, err = a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msgDoc}})
	if err == nil {
		t.Fatalf("expected error")
	}
	if !errors.As(err, &ce) {
		t.Fatalf("expected ConfigurationError, got %T (%v)", err, err)
	}
}

func TestAdapter_Complete_HTTPErrorMapping_ServerErrorWithRetryAfter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":{"message":"unavailable"}}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var se *llm.ServerError
	if !errors.As(err, &se) {
		t.Fatalf("expected ServerError, got %T (%v)", err, err)
	}
	if !se.Retryable() {
		t.Fatalf("expected retryable server error")
	}
	if se.RetryAfter() == nil || *se.RetryAfter() != 1*time.Second {
		t.Fatalf("retry_after: %v", se.RetryAfter())
	}
}

func TestAdapter_Stream_YieldsTextDeltasAndFinish(t *testing.T) {
	var gotBody map[string]any
	gotKey := ""
	gotPath := ""
	gotAlt := ""

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotAlt = r.URL.Query().Get("alt")
		gotPath = r.URL.Path
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		_ = json.Unmarshal(b, &gotBody)

		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(data string) {
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write(`{"candidates":[{"content":{"parts":[{"text":"Hel"}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		write(`{"candidates":[{"content":{"parts":[{"text":"lo"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
	if gotKey != "k" {
		t.Fatalf("key param: %q", gotKey)
	}
	if gotAlt != "sse" {
		t.Fatalf("alt param: %q", gotAlt)
	}
	if !strings.Contains(gotPath, "/v1beta/models/") {
		t.Fatalf("path: %q", gotPath)
	}
	if gotBody == nil {
		t.Fatalf("server did not capture request body")
	}
	if _, ok := gotBody["contents"].([]any); !ok {
		t.Fatalf("contents: %#v", gotBody["contents"])
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

func TestAdapter_Stream_TranslatesFunctionCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		write := func(data string) {
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
			if f != nil {
				f.Flush()
			}
		}

		write(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"n":1}}}]}}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`)
		write(`{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}`)
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var startID, endID string
	var endArgs string
	var finishResp *llm.Response
	var kinds []llm.StreamEventType
	for ev := range stream.Events() {
		kinds = append(kinds, ev.Type)
		switch ev.Type {
		case llm.StreamEventToolCallStart:
			if ev.ToolCall != nil {
				startID = ev.ToolCall.ID
			}
		case llm.StreamEventToolCallEnd:
			if ev.ToolCall != nil {
				endID = ev.ToolCall.ID
				endArgs = string(ev.ToolCall.Arguments)
			}
		case llm.StreamEventFinish:
			if ev.Response != nil {
				finishResp = ev.Response
			}
		}
	}
	if strings.TrimSpace(startID) == "" || strings.TrimSpace(endID) == "" || startID != endID {
		t.Fatalf("expected stable synthetic call id; start=%q end=%q", startID, endID)
	}
	if strings.TrimSpace(endArgs) != `{"n":1}` {
		t.Fatalf("tool args: %q", endArgs)
	}
	if finishResp == nil {
		t.Fatalf("expected finish response")
	}
	calls := finishResp.ToolCalls()
	if len(calls) != 1 {
		t.Fatalf("finish tool calls: %+v", calls)
	}
	if calls[0].ID != startID || calls[0].Name != "get_weather" {
		t.Fatalf("finish tool call: %+v", calls[0])
	}
	if strings.TrimSpace(string(calls[0].Arguments)) != `{"n":1}` {
		t.Fatalf("finish tool call args: %q", string(calls[0].Arguments))
	}
	foundStart := false
	foundEnd := false
	for _, k := range kinds {
		if k == llm.StreamEventToolCallStart {
			foundStart = true
		}
		if k == llm.StreamEventToolCallEnd {
			foundEnd = true
		}
	}
	if !foundStart || !foundEnd {
		t.Fatalf("expected TOOL_CALL_START and TOOL_CALL_END events (kinds=%v)", kinds)
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
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
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/x.png", MediaType: "image/png"}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{MediaType: "image/png", Data: []byte{0x01, 0x02, 0x03}}},
			{Kind: llm.ContentImage, Image: &llm.ImageData{URL: imgPath}},
		},
	}
	if _, err := a.Complete(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{msg}}); err != nil {
		t.Fatalf("Complete: %v", err)
	}

	contents, ok := gotBody["contents"].([]any)
	if !ok || len(contents) == 0 {
		t.Fatalf("contents: %#v", gotBody["contents"])
	}
	first, _ := contents[0].(map[string]any)
	parts, _ := first["parts"].([]any)
	if len(parts) == 0 {
		t.Fatalf("parts: %#v", first["parts"])
	}

	seenURL := false
	seenInline := 0
	for _, pAny := range parts {
		p, ok := pAny.(map[string]any)
		if !ok {
			continue
		}
		if fd, ok := p["fileData"].(map[string]any); ok {
			if fd["fileUri"] == "https://example.com/x.png" {
				seenURL = true
			}
		}
		if _, ok := p["inlineData"].(map[string]any); ok {
			seenInline++
		}
	}
	if !seenURL || seenInline < 2 {
		t.Fatalf("expected url image + 2 inline images; seenURL=%v seenInline=%d parts=%#v", seenURL, seenInline, parts)
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
  "candidates": [{"content": {"parts": [{"text":"{}"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
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
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		ResponseFormat: &llm.ResponseFormat{
			Type:       "json_schema",
			JSONSchema: schema,
		},
	})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}

	genCfg, ok := gotBody["generationConfig"].(map[string]any)
	if !ok || genCfg == nil {
		t.Fatalf("generationConfig: %#v", gotBody["generationConfig"])
	}
	if genCfg["responseMimeType"] != "application/json" {
		t.Fatalf("responseMimeType: %#v", genCfg["responseMimeType"])
	}
	if _, ok := genCfg["responseSchema"].(map[string]any); !ok {
		t.Fatalf("responseSchema: %#v", genCfg["responseSchema"])
	}
}

func TestAdapter_Stream_ContextDeadline_EmitsRequestTimeoutError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, ":streamGenerateContent") {
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

	st, err := a.Stream(ctx, llm.Request{Model: "gemini-test", Messages: []llm.Message{llm.User("hi")}})
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
  "candidates": [{"content": {"parts": [{"text":"ok"}]}, "finishReason":"STOP"}],
  "usageMetadata": {"promptTokenCount": 1, "candidatesTokenCount": 1, "totalTokenCount": 2}
}`))
	}))
	t.Cleanup(srv.Close)

	a := &Adapter{APIKey: "k", BaseURL: srv.URL, Client: srv.Client()}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := a.Complete(ctx, llm.Request{
		Model:    "gemini-test",
		Messages: []llm.Message{llm.User("hi")},
		ProviderOptions: map[string]any{
			"google": map[string]any{
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
