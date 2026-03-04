package codexappserver

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danshapiro/kilroy/internal/llm"
)

func mustTranscriptPayload(t *testing.T, params map[string]any) map[string]any {
	t.Helper()
	input := asSlice(params["input"])
	if len(input) == 0 {
		t.Fatalf("missing input")
	}
	textItem := asMap(input[0])
	if asString(textItem["type"]) != "text" {
		t.Fatalf("first input item is not text: %#v", textItem)
	}
	transcript := asString(textItem["text"])
	if !strings.Contains(transcript, transcriptPayloadBeginMarker) || !strings.Contains(transcript, transcriptPayloadEndMarker) {
		t.Fatalf("missing transcript payload markers")
	}
	start := strings.Index(transcript, transcriptPayloadBeginMarker+"\n")
	if start < 0 {
		t.Fatalf("missing payload start marker")
	}
	start += len(transcriptPayloadBeginMarker) + 1
	end := strings.Index(transcript[start:], "\n"+transcriptPayloadEndMarker)
	if end < 0 {
		t.Fatalf("missing payload end marker")
	}
	payloadJSON := transcript[start : start+end]
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("payload json unmarshal: %v", err)
	}
	return payload
}

func TestTranslateRequest_FullSurface(t *testing.T) {
	temperature := 0.3
	topP := 0.8
	maxTokens := 300
	reasoning := "high"

	request := llm.Request{
		Model: "gpt-5.2-codex",
		Messages: []llm.Message{
			llm.System("System guardrails"),
			llm.Developer("Developer instruction"),
			{
				Role: llm.RoleUser,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "What is in this image?"},
					{Kind: llm.ContentImage, Image: &llm.ImageData{URL: "https://example.com/cat.png", Detail: "high"}},
				},
			},
			{
				Role: llm.RoleAssistant,
				Content: []llm.ContentPart{
					{Kind: llm.ContentText, Text: "Let me inspect it."},
					{Kind: llm.ContentToolCall, ToolCall: &llm.ToolCallData{ID: "call_weather", Name: "get_weather", Arguments: json.RawMessage(`{"city":"SF"}`)}},
				},
			},
			llm.ToolResultNamed("call_weather", "get_weather", map[string]any{"temperature": "72F"}, false),
		},
		Tools: []llm.ToolDefinition{{
			Name:        "get_weather",
			Description: "Get weather for a city",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{"city": map[string]any{"type": "string"}},
				"required":   []any{"city"},
			},
		}},
		ToolChoice: &llm.ToolChoice{Mode: "named", Name: "get_weather"},
		ResponseFormat: &llm.ResponseFormat{
			Type: "json_schema",
			JSONSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{"answer": map[string]any{"type": "string"}},
				"required":   []any{"answer"},
			},
		},
		Temperature:     &temperature,
		TopP:            &topP,
		MaxTokens:       &maxTokens,
		StopSequences:   []string{"<END>"},
		ReasoningEffort: &reasoning,
		Metadata: map[string]string{
			"traceId": "trace-123",
			"tenant":  "acme",
		},
		ProviderOptions: map[string]any{
			"codex_app_server": map[string]any{
				"cwd":         "/tmp/project",
				"summary":     "concise",
				"personality": "pragmatic",
			},
		},
	}

	translated, err := translateRequest(request, false)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	if len(translated.Warnings) != 0 {
		t.Fatalf("unexpected warnings: %+v", translated.Warnings)
	}
	params := translated.Payload
	if got := asString(params["threadId"]); got != defaultThreadID {
		t.Fatalf("threadId: got %q want %q", got, defaultThreadID)
	}
	if got := asString(params["model"]); got != "gpt-5.2-codex" {
		t.Fatalf("model: got %q", got)
	}
	if got := asString(params["cwd"]); got != "/tmp/project" {
		t.Fatalf("cwd: got %q", got)
	}
	if got := asString(params["summary"]); got != "concise" {
		t.Fatalf("summary: got %q", got)
	}
	if got := asString(params["personality"]); got != "pragmatic" {
		t.Fatalf("personality: got %q", got)
	}
	if params["outputSchema"] == nil {
		t.Fatalf("expected outputSchema to be set")
	}

	input := asSlice(params["input"])
	if len(input) != 2 {
		t.Fatalf("input len: got %d want 2", len(input))
	}
	imageInput := asMap(input[1])
	if asString(imageInput["type"]) != "image" || asString(imageInput["url"]) != "https://example.com/cat.png" {
		t.Fatalf("image input mismatch: %#v", imageInput)
	}

	payload := mustTranscriptPayload(t, params)
	if got := asString(payload["version"]); got != transcriptVersion {
		t.Fatalf("payload version: got %q want %q", got, transcriptVersion)
	}
	controls := asMap(payload["controls"])
	if got := asString(controls["model"]); got != "gpt-5.2-codex" {
		t.Fatalf("controls.model: got %q", got)
	}
	if got := asString(asMap(controls["toolChoice"])["mode"]); got != "named" {
		t.Fatalf("tool choice mode: got %q", got)
	}
	if got := asString(asMap(controls["toolChoice"])["toolName"]); got != "get_weather" {
		t.Fatalf("tool choice name: got %q", got)
	}
}

func TestTranslateRequest_FallbackWarnings(t *testing.T) {
	request := llm.Request{
		Model: "codex-mini",
		Messages: []llm.Message{{
			Role: llm.RoleUser,
			Content: []llm.ContentPart{
				{Kind: llm.ContentAudio, Audio: &llm.AudioData{URL: "https://example.com/a.wav", MediaType: "audio/wav"}},
				{Kind: llm.ContentDocument, Document: &llm.DocumentData{URL: "https://example.com/r.pdf", MediaType: "application/pdf", FileName: "r.pdf"}},
				{Kind: llm.ContentKind("custom_note"), Data: map[string]any{"topic": "ops", "priority": "high"}},
			},
		}},
	}

	translated, err := translateRequest(request, false)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	if len(translated.Warnings) != 3 {
		t.Fatalf("warning len: got %d want 3", len(translated.Warnings))
	}
	for _, w := range translated.Warnings {
		if w.Code != "unsupported_part" {
			t.Fatalf("warning code: got %q want unsupported_part", w.Code)
		}
	}
	if translated.Warnings[2].Message != "Custom (custom_note) content parts are not natively supported by codex-app-server and were translated to deterministic transcript fallback text" {
		t.Fatalf("custom warning mismatch: %q", translated.Warnings[2].Message)
	}

	payload := mustTranscriptPayload(t, translated.Payload)
	history := asSlice(payload["history"])
	if len(history) != 1 {
		t.Fatalf("history len: got %d want 1", len(history))
	}
	parts := asSlice(asMap(history[0])["parts"])
	if len(parts) != 3 {
		t.Fatalf("parts len: got %d want 3", len(parts))
	}
	customPart := asMap(parts[2])
	if got := asString(customPart["fallbackKind"]); got != "custom" {
		t.Fatalf("fallbackKind: got %q want custom", got)
	}
	customData := asMap(customPart["data"])
	if asString(customData["topic"]) != "ops" || asString(customData["priority"]) != "high" {
		t.Fatalf("custom data mismatch: %#v", customData)
	}
}

func TestTranslateRequest_ValidatesToolChoice(t *testing.T) {
	req := llm.Request{
		Model:      "codex-mini",
		Messages:   []llm.Message{llm.User("Need tools")},
		ToolChoice: &llm.ToolChoice{Mode: "required"},
	}
	if _, err := translateRequest(req, false); err == nil {
		t.Fatalf("expected error for required tool choice without tools")
	}

	req = llm.Request{
		Model:    "codex-mini",
		Messages: []llm.Message{llm.User("Need weather")},
		Tools: []llm.ToolDefinition{{
			Name:       "lookup_weather",
			Parameters: map[string]any{"type": "object"},
		}},
		ToolChoice: &llm.ToolChoice{Mode: "named", Name: "missing_tool"},
	}
	if _, err := translateRequest(req, false); err == nil {
		t.Fatalf("expected error for named tool choice without matching tool")
	}
}

func TestTranslateRequest_DefaultReasoningEffort(t *testing.T) {
	translated, err := translateRequest(llm.Request{Model: "codex-mini", Messages: []llm.Message{llm.User("Hello")}}, false)
	if err != nil {
		t.Fatalf("translateRequest: %v", err)
	}
	if got := asString(translated.Payload["effort"]); got != "high" {
		t.Fatalf("effort: got %q want high", got)
	}
}
