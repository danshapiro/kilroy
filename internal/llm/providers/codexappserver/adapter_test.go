package codexappserver

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

type fakeTransport struct {
	initializeFn func(ctx context.Context) error
	closeFn      func() error
	completeFn   func(ctx context.Context, payload map[string]any) (map[string]any, error)
	streamFn     func(ctx context.Context, payload map[string]any) (*NotificationStream, error)
	listFn       func(ctx context.Context, params map[string]any) (modelListResponse, error)
}

func (f *fakeTransport) Initialize(ctx context.Context) error {
	if f.initializeFn != nil {
		return f.initializeFn(ctx)
	}
	return nil
}

func (f *fakeTransport) Close() error {
	if f.closeFn != nil {
		return f.closeFn()
	}
	return nil
}

func (f *fakeTransport) Complete(ctx context.Context, payload map[string]any) (map[string]any, error) {
	if f.completeFn != nil {
		return f.completeFn(ctx, payload)
	}
	return map[string]any{}, nil
}

func (f *fakeTransport) Stream(ctx context.Context, payload map[string]any) (*NotificationStream, error) {
	if f.streamFn != nil {
		return f.streamFn(ctx, payload)
	}
	events := make(chan map[string]any)
	errs := make(chan error)
	close(events)
	close(errs)
	return &NotificationStream{Notifications: events, Err: errs, closeFn: func() {}}, nil
}

func (f *fakeTransport) ListModels(ctx context.Context, params map[string]any) (modelListResponse, error) {
	if f.listFn != nil {
		return f.listFn(ctx, params)
	}
	return modelListResponse{Data: []modelEntry{}, NextCursor: nil}, nil
}

func TestAdapterComplete_UsesTransportAndMergesWarnings(t *testing.T) {
	var seenPayload map[string]any
	transport := &fakeTransport{
		completeFn: func(ctx context.Context, payload map[string]any) (map[string]any, error) {
			seenPayload = payload
			return map[string]any{"turn": map[string]any{"id": "turn_1", "status": "completed", "items": []any{}}}, nil
		},
	}
	adapter := NewAdapter(AdapterOptions{
		Transport: transport,
		TranslateRequest: func(request llm.Request, streaming bool) (translateRequestResult, error) {
			return translateRequestResult{
				Payload:  map[string]any{"input": []any{}, "threadId": defaultThreadID},
				Warnings: []llm.Warning{{Message: "Dropped unsupported audio", Code: "unsupported_part"}},
			}, nil
		},
		TranslateResponse: func(body map[string]any) (llm.Response, error) {
			return llm.Response{
				ID:       "resp_1",
				Model:    "codex-mini",
				Provider: providerName,
				Message:  llm.Assistant("done"),
				Finish:   llm.FinishReason{Reason: llm.FinishReasonStop},
				Usage:    llm.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
				Warnings: []llm.Warning{{Message: "Deprecated field"}},
			}, nil
		},
	})

	resp, err := adapter.Complete(context.Background(), llm.Request{Model: "codex-mini", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if seenPayload == nil {
		t.Fatalf("transport payload not captured")
	}
	if len(resp.Warnings) != 2 {
		t.Fatalf("warnings len: got %d want 2", len(resp.Warnings))
	}
	if resp.Warnings[0].Message != "Deprecated field" || resp.Warnings[1].Code != "unsupported_part" {
		t.Fatalf("warnings mismatch: %+v", resp.Warnings)
	}
}

func TestAdapterComplete_MapsTurnErrors(t *testing.T) {
	transport := &fakeTransport{
		completeFn: func(ctx context.Context, payload map[string]any) (map[string]any, error) {
			return map[string]any{
				"turn": map[string]any{
					"id":     "turn_bad",
					"status": "failed",
					"error": map[string]any{
						"status":  429,
						"code":    "RATE_LIMITED",
						"message": "too many requests",
					},
				},
			}, nil
		},
	}
	adapter := NewAdapter(AdapterOptions{
		Transport: transport,
		TranslateRequest: func(request llm.Request, streaming bool) (translateRequestResult, error) {
			return translateRequestResult{Payload: map[string]any{"input": []any{}, "threadId": defaultThreadID}}, nil
		},
		TranslateResponse: func(body map[string]any) (llm.Response, error) {
			return llm.Response{}, nil
		},
	})

	_, err := adapter.Complete(context.Background(), llm.Request{Model: "codex-mini", Messages: []llm.Message{llm.User("hello")}})
	if err == nil {
		t.Fatalf("expected complete error")
	}
	var rateLimit *llm.RateLimitError
	if !errors.As(err, &rateLimit) {
		t.Fatalf("expected RateLimitError, got %T (%v)", err, err)
	}
}

func TestAdapterStream_AttachesWarningsToStreamStart(t *testing.T) {
	transport := &fakeTransport{
		streamFn: func(ctx context.Context, payload map[string]any) (*NotificationStream, error) {
			events := make(chan map[string]any, 2)
			errs := make(chan error, 1)
			events <- map[string]any{"method": "turn/started", "params": map[string]any{"turn": map[string]any{"id": "turn_1", "status": "inProgress", "items": []any{}}}}
			events <- map[string]any{"method": "turn/completed", "params": map[string]any{"turn": map[string]any{"id": "turn_1", "status": "completed", "items": []any{}}}}
			close(events)
			close(errs)
			return &NotificationStream{Notifications: events, Err: errs, closeFn: func() {}}, nil
		},
	}
	adapter := NewAdapter(AdapterOptions{
		Transport: transport,
		TranslateRequest: func(request llm.Request, streaming bool) (translateRequestResult, error) {
			return translateRequestResult{
				Payload:  map[string]any{"input": []any{}, "threadId": defaultThreadID},
				Warnings: []llm.Warning{{Message: "Tool output truncated", Code: "truncated"}},
			}, nil
		},
	})

	stream, err := adapter.Stream(context.Background(), llm.Request{Model: "codex-mini", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var start *llm.StreamEvent
	for event := range stream.Events() {
		if event.Type == llm.StreamEventStreamStart {
			copyEvent := event
			start = &copyEvent
		}
	}
	if start == nil {
		t.Fatalf("expected stream start event")
	}
	if len(start.Warnings) != 1 || start.Warnings[0].Code != "truncated" {
		t.Fatalf("stream start warnings mismatch: %+v", start.Warnings)
	}
}

func TestAdapter_ListModelsCachesFirstResponse(t *testing.T) {
	calls := 0
	transport := &fakeTransport{
		listFn: func(ctx context.Context, params map[string]any) (modelListResponse, error) {
			calls++
			return modelListResponse{Data: []modelEntry{{ID: "1", Model: "codex-mini", IsDefault: true}}}, nil
		},
	}
	adapter := NewAdapter(AdapterOptions{Transport: transport})

	first, err := adapter.ListModels(context.Background(), map[string]any{"limit": 1})
	if err != nil {
		t.Fatalf("ListModels first: %v", err)
	}
	second, err := adapter.ListModels(context.Background(), map[string]any{"limit": 99})
	if err != nil {
		t.Fatalf("ListModels second: %v", err)
	}
	if calls != 1 {
		t.Fatalf("list call count: got %d want 1", calls)
	}
	if len(first.Data) != 1 || len(second.Data) != 1 {
		t.Fatalf("cached model list mismatch: first=%+v second=%+v", first, second)
	}
}

func TestResolveFileImages_LoadsLocalPathData(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/image.png"
	if err := os.WriteFile(path, []byte{0x89, 0x50, 0x4e, 0x47}, 0o644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	resolved, err := resolveFileImages(llm.Request{
		Model: "codex-mini",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{{
				Kind:  llm.ContentImage,
				Image: &llm.ImageData{URL: path},
			}},
		}},
	})
	if err != nil {
		t.Fatalf("resolveFileImages: %v", err)
	}
	part := resolved.Messages[0].Content[0]
	if part.Image == nil || len(part.Image.Data) == 0 || part.Image.URL != "" {
		t.Fatalf("resolved image mismatch: %+v", part.Image)
	}
	if part.Image.MediaType != "image/png" {
		t.Fatalf("media type mismatch: got %q want %q", part.Image.MediaType, "image/png")
	}
}

func TestMapCodexError_UnsupportedModelMessageBecomesInvalidRequest(t *testing.T) {
	err := mapCodexError(map[string]any{
		"turn": map[string]any{
			"id":     "turn_unsupported",
			"status": "failed",
			"error": map[string]any{
				"message": "{\"detail\":\"The 'nonexistent-model-xyz' model is not supported when using Codex with a ChatGPT account.\"}",
			},
		},
	}, providerName, "complete")

	var invalid *llm.InvalidRequestError
	if !errors.As(err, &invalid) {
		t.Fatalf("expected InvalidRequestError, got %T (%v)", err, err)
	}
}

func TestAdapterStream_PropagatesTransportErrors(t *testing.T) {
	transport := &fakeTransport{
		streamFn: func(ctx context.Context, payload map[string]any) (*NotificationStream, error) {
			events := make(chan map[string]any)
			errs := make(chan error, 1)
			close(events)
			errs <- context.DeadlineExceeded
			close(errs)
			return &NotificationStream{Notifications: events, Err: errs, closeFn: func() {}}, nil
		},
	}
	adapter := NewAdapter(AdapterOptions{
		Transport: transport,
		TranslateRequest: func(request llm.Request, streaming bool) (translateRequestResult, error) {
			return translateRequestResult{Payload: map[string]any{"input": []any{}, "threadId": defaultThreadID}}, nil
		},
		TranslateStream: func(events <-chan map[string]any) <-chan llm.StreamEvent {
			out := make(chan llm.StreamEvent)
			close(out)
			return out
		},
	})

	stream, err := adapter.Stream(context.Background(), llm.Request{Model: "codex-mini", Messages: []llm.Message{llm.User("hello")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()

	var gotErr error
	timeout := time.After(2 * time.Second)
loop:
	for {
		select {
		case event, ok := <-stream.Events():
			if !ok {
				break loop
			}
			if event.Type == llm.StreamEventError {
				gotErr = event.Err
			}
		case <-timeout:
			t.Fatalf("timed out waiting for stream events")
		}
	}
	if gotErr == nil {
		t.Fatalf("expected stream error event")
	}
	var timeoutErr *llm.RequestTimeoutError
	if !errors.As(gotErr, &timeoutErr) {
		t.Fatalf("expected RequestTimeoutError, got %T (%v)", gotErr, gotErr)
	}
}

var _ codexTransport = (*fakeTransport)(nil)

func TestNormalizeErrorInfo_UnwrapsJSONMessage(t *testing.T) {
	info := normalizeErrorInfo(map[string]any{"message": `{"detail":"wrapped detail"}`})
	if info.Message != "wrapped detail" {
		t.Fatalf("message: got %q want %q", info.Message, "wrapped detail")
	}
}

func TestNormalizeErrorInfo_IgnoresSymbolicStatus(t *testing.T) {
	info := normalizeErrorInfo(map[string]any{
		"error": map[string]any{
			"status":  "RESOURCE_EXHAUSTED",
			"message": "rate limited",
		},
	})
	if info.HasStatus {
		t.Fatalf("expected symbolic status to be ignored, got status=%d", info.Status)
	}
}

func TestNormalizeErrorInfo_ParsesNumericStatusString(t *testing.T) {
	info := normalizeErrorInfo(map[string]any{
		"error": map[string]any{
			"status":  "429",
			"message": "rate limited",
		},
	})
	if !info.HasStatus || info.Status != 429 {
		t.Fatalf("expected HTTP status 429, got hasStatus=%v status=%d", info.HasStatus, info.Status)
	}
}

func TestParseToolCall_NormalizesArguments(t *testing.T) {
	tool := parseToolCall(`{"id":"call_1","name":"search","arguments":{"q":"foo"}}`)
	if tool == nil {
		t.Fatalf("expected tool call")
	}
	if tool.ID != "call_1" || tool.Name != "search" {
		t.Fatalf("tool metadata mismatch: %+v", tool)
	}
	if strings.TrimSpace(string(tool.Arguments)) != `{"q":"foo"}` {
		t.Fatalf("tool args mismatch: %q", string(tool.Arguments))
	}
	if !json.Valid(tool.Arguments) {
		t.Fatalf("tool args should be valid json: %q", string(tool.Arguments))
	}
}
