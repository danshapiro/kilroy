package codexappserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/danshapiro/kilroy/internal/llm"
)

const (
	transcriptBeginMarker        = "[[[UNIFIED_TRANSCRIPT_V1_BEGIN]]]"
	transcriptEndMarker          = "[[[UNIFIED_TRANSCRIPT_V1_END]]]"
	transcriptPayloadBeginMarker = "[[[UNIFIED_TRANSCRIPT_PAYLOAD_BEGIN]]]"
	transcriptPayloadEndMarker   = "[[[UNIFIED_TRANSCRIPT_PAYLOAD_END]]]"
	toolCallBeginMarker          = "[[TOOL_CALL]]"
	toolCallEndMarker            = "[[/TOOL_CALL]]"
	defaultThreadID              = "thread_stateless"
	transcriptVersion            = "unified.codex-app-server.request.v1"
	defaultReasoningEffort       = "high"
)

var (
	jsonObjectOutputSchema = map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": true,
	}
	supportedReasoningEfforts = map[string]struct{}{
		"none":    {},
		"minimal": {},
		"low":     {},
		"medium":  {},
		"high":    {},
		"xhigh":   {},
	}
	turnOptionKeyMap = map[string]string{
		"cwd":                "cwd",
		"approvalPolicy":     "approvalPolicy",
		"approval_policy":    "approvalPolicy",
		"sandboxPolicy":      "sandboxPolicy",
		"sandbox_policy":     "sandboxPolicy",
		"model":              "model",
		"effort":             "effort",
		"summary":            "summary",
		"personality":        "personality",
		"collaborationMode":  "collaborationMode",
		"collaboration_mode": "collaborationMode",
		"outputSchema":       "outputSchema",
		"output_schema":      "outputSchema",
	}
	controlOptionKeyMap = map[string]string{
		"temperature":      "temperature",
		"topP":             "topP",
		"top_p":            "topP",
		"maxTokens":        "maxTokens",
		"max_tokens":       "maxTokens",
		"stopSequences":    "stopSequences",
		"stop_sequences":   "stopSequences",
		"metadata":         "metadata",
		"reasoningEffort":  "reasoningEffort",
		"reasoning_effort": "reasoningEffort",
	}
	uriSchemeRE = regexp.MustCompile(`^[a-zA-Z][a-zA-Z\d+\-.]*:`)
)

type resolvedToolChoice struct {
	Mode     string `json:"mode"`
	ToolName string `json:"toolName,omitempty"`
}

type transcriptControls struct {
	Model          string                 `json:"model"`
	ToolChoice     resolvedToolChoice     `json:"toolChoice"`
	ResponseFormat map[string]any         `json:"responseFormat"`
	Temperature    *float64               `json:"temperature,omitempty"`
	TopP           *float64               `json:"topP,omitempty"`
	MaxTokens      *int                   `json:"maxTokens,omitempty"`
	StopSequences  []string               `json:"stopSequences,omitempty"`
	ReasoningEff   string                 `json:"reasoningEffort,omitempty"`
	Metadata       map[string]interface{} `json:"metadata,omitempty"`
}

type transcriptPayload struct {
	Version          string             `json:"version"`
	ToolCallProtocol map[string]string  `json:"toolCallProtocol"`
	Controls         transcriptControls `json:"controls"`
	Tools            []map[string]any   `json:"tools"`
	History          []map[string]any   `json:"history"`
}

type translateRequestResult struct {
	Payload  map[string]any
	Warnings []llm.Warning
}

func translateRequest(request llm.Request, _ bool) (translateRequestResult, error) {
	warnings := make([]llm.Warning, 0, 4)
	toolChoice := normalizeToolChoice(request)
	if err := validateToolChoice(toolChoice, request); err != nil {
		return translateRequestResult{}, err
	}

	var reasoningInput string
	if request.ReasoningEffort != nil {
		reasoningInput = *request.ReasoningEffort
	}
	reasoningEffort := normalizeReasoningEffort(reasoningInput, &warnings, "request.reasoningEffort")
	if reasoningEffort == "" {
		reasoningEffort = defaultReasoningEffort
	}

	controls := transcriptControls{
		Model:          request.Model,
		ToolChoice:     toolChoice,
		ResponseFormat: responseFormatForTranscript(request.ResponseFormat),
		Temperature:    request.Temperature,
		TopP:           request.TopP,
		MaxTokens:      request.MaxTokens,
		StopSequences:  append([]string{}, request.StopSequences...),
		ReasoningEff:   reasoningEffort,
		Metadata:       metadataForTranscript(request.Metadata),
	}

	history, imageInputs := translateMessages(request.Messages, &warnings)

	params := map[string]any{
		"threadId": defaultThreadID,
		"model":    request.Model,
		"effort":   controls.ReasoningEff,
	}
	if outputSchema := resolveOutputSchema(request.ResponseFormat); outputSchema != nil {
		params["outputSchema"] = outputSchema
	}

	applyProviderOptions(request, params, &controls, &warnings)
	if model := strings.TrimSpace(asString(params["model"])); model != "" {
		controls.Model = model
	}
	paramEffort := normalizeReasoningEffort(asString(params["effort"]), &warnings, "codex_app_server.effort")
	if paramEffort == "" {
		paramEffort = controls.ReasoningEff
	}
	if paramEffort == "" {
		paramEffort = defaultReasoningEffort
	}
	params["effort"] = paramEffort
	controls.ReasoningEff = paramEffort

	payload := transcriptPayload{
		Version: transcriptVersion,
		ToolCallProtocol: map[string]string{
			"beginMarker": toolCallBeginMarker,
			"endMarker":   toolCallEndMarker,
		},
		Controls: controls,
		Tools:    buildToolsSection(request),
		History:  history,
	}

	transcript, err := buildTranscript(payload, toolChoice)
	if err != nil {
		return translateRequestResult{}, err
	}

	input := make([]any, 0, 1+len(imageInputs))
	input = append(input, map[string]any{
		"type":          "text",
		"text":          transcript,
		"text_elements": []any{},
	})
	for _, in := range imageInputs {
		input = append(input, in)
	}
	params["input"] = input

	return translateRequestResult{Payload: params, Warnings: warnings}, nil
}

func normalizeToolChoice(request llm.Request) resolvedToolChoice {
	if request.ToolChoice != nil {
		mode := strings.TrimSpace(strings.ToLower(request.ToolChoice.Mode))
		if mode == "named" {
			return resolvedToolChoice{Mode: "named", ToolName: strings.TrimSpace(request.ToolChoice.Name)}
		}
		if mode == "" {
			mode = "auto"
		}
		return resolvedToolChoice{Mode: mode}
	}
	if len(request.Tools) > 0 {
		return resolvedToolChoice{Mode: "auto"}
	}
	return resolvedToolChoice{Mode: "none"}
}

func validateToolChoice(choice resolvedToolChoice, request llm.Request) error {
	toolNames := make(map[string]struct{}, len(request.Tools))
	for _, tool := range request.Tools {
		toolNames[strings.TrimSpace(tool.Name)] = struct{}{}
	}
	switch choice.Mode {
	case "required":
		if len(toolNames) == 0 {
			return fmt.Errorf("toolChoice.mode=\"required\" requires at least one tool definition")
		}
	case "named":
		if len(toolNames) == 0 {
			return fmt.Errorf("toolChoice.mode=\"named\" requires tools, but no tools were provided")
		}
		if strings.TrimSpace(choice.ToolName) == "" {
			return fmt.Errorf("toolChoice.mode=\"named\" requires a non-empty toolName")
		}
		if _, ok := toolNames[choice.ToolName]; !ok {
			return fmt.Errorf("toolChoice.mode=\"named\" references unknown tool %q", choice.ToolName)
		}
	}
	return nil
}

func responseFormatForTranscript(format *llm.ResponseFormat) map[string]any {
	if format == nil {
		return map[string]any{"type": "text"}
	}
	out := map[string]any{"type": format.Type}
	if format.JSONSchema != nil {
		out["jsonSchema"] = format.JSONSchema
	}
	if format.Strict {
		out["strict"] = true
	}
	return out
}

func metadataForTranscript(metadata map[string]string) map[string]interface{} {
	if len(metadata) == 0 {
		return nil
	}
	out := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		out[key] = value
	}
	return out
}

func resolveOutputSchema(responseFormat *llm.ResponseFormat) map[string]any {
	if responseFormat == nil || strings.EqualFold(responseFormat.Type, "text") || strings.TrimSpace(responseFormat.Type) == "" {
		return nil
	}
	if strings.EqualFold(responseFormat.Type, "json") {
		return deepCopyMap(jsonObjectOutputSchema)
	}
	if responseFormat.JSONSchema == nil {
		return nil
	}
	return deepCopyMap(responseFormat.JSONSchema)
}

func buildToolsSection(request llm.Request) []map[string]any {
	if len(request.Tools) == 0 {
		return nil
	}
	out := make([]map[string]any, 0, len(request.Tools))
	for _, tool := range request.Tools {
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  tool.Parameters,
		})
	}
	return out
}

func translateMessages(messages []llm.Message, warnings *[]llm.Warning) ([]map[string]any, []map[string]any) {
	history := make([]map[string]any, 0, len(messages))
	imageInputs := make([]map[string]any, 0, 2)
	imageIndex := 0
	nextImageID := func() string {
		imageIndex++
		return fmt.Sprintf("img_%04d", imageIndex)
	}

	for messageIndex, message := range messages {
		parts := make([]map[string]any, 0, len(message.Content))
		for partIndex, part := range message.Content {
			parts = append(parts, translatePart(part, messageIndex, partIndex, warnings, &imageInputs, nextImageID))
		}
		history = append(history, map[string]any{
			"index":      messageIndex,
			"role":       string(message.Role),
			"name":       message.Name,
			"toolCallId": message.ToolCallID,
			"parts":      parts,
		})
	}

	return history, imageInputs
}

func translatePart(
	part llm.ContentPart,
	messageIndex int,
	partIndex int,
	warnings *[]llm.Warning,
	imageInputs *[]map[string]any,
	nextImageID func() string,
) map[string]any {
	switch part.Kind {
	case llm.ContentText:
		return map[string]any{
			"index": partIndex,
			"kind":  "text",
			"text":  part.Text,
		}
	case llm.ContentImage:
		imageID := nextImageID()
		if part.Image != nil {
			if len(part.Image.Data) > 0 {
				mediaType := strings.TrimSpace(part.Image.MediaType)
				if mediaType == "" {
					mediaType = "image/png"
				}
				*imageInputs = append(*imageInputs, map[string]any{
					"type": "image",
					"url":  llm.DataURI(mediaType, part.Image.Data),
				})
				return map[string]any{
					"index":     partIndex,
					"kind":      "image",
					"assetId":   imageID,
					"inputType": "image",
					"source":    "inline_data",
					"mediaType": mediaType,
					"detail":    part.Image.Detail,
				}
			}
			if url := strings.TrimSpace(part.Image.URL); url != "" {
				if isLikelyLocalPath(url) {
					*imageInputs = append(*imageInputs, map[string]any{"type": "localImage", "path": url})
					return map[string]any{
						"index":     partIndex,
						"kind":      "image",
						"assetId":   imageID,
						"inputType": "localImage",
						"source":    "local_path",
						"path":      url,
						"detail":    part.Image.Detail,
					}
				}
				*imageInputs = append(*imageInputs, map[string]any{"type": "image", "url": url})
				return map[string]any{
					"index":     partIndex,
					"kind":      "image",
					"assetId":   imageID,
					"inputType": "image",
					"source":    "remote_url",
					"url":       url,
					"detail":    part.Image.Detail,
				}
			}
		}
		*warnings = append(*warnings, llm.Warning{
			Code:    "unsupported_part",
			Message: "Image content parts without data or url cannot be attached and were translated to fallback text",
		})
		return map[string]any{
			"index":    partIndex,
			"kind":     "image",
			"assetId":  imageID,
			"fallback": "missing_image_data_or_url",
		}
	case llm.ContentAudio:
		*warnings = append(*warnings, warningForFallback("Audio"))
		byteLength := 0
		url := ""
		mediaType := ""
		if part.Audio != nil {
			byteLength = len(part.Audio.Data)
			url = part.Audio.URL
			mediaType = part.Audio.MediaType
		}
		return map[string]any{
			"index": partIndex,
			"kind":  "audio",
			"fallback": map[string]any{
				"url":        url,
				"mediaType":  mediaType,
				"byteLength": byteLength,
			},
		}
	case llm.ContentDocument:
		*warnings = append(*warnings, warningForFallback("Document"))
		byteLength := 0
		url := ""
		mediaType := ""
		filename := ""
		if part.Document != nil {
			byteLength = len(part.Document.Data)
			url = part.Document.URL
			mediaType = part.Document.MediaType
			filename = part.Document.FileName
		}
		return map[string]any{
			"index": partIndex,
			"kind":  "document",
			"fallback": map[string]any{
				"url":        url,
				"mediaType":  mediaType,
				"fileName":   filename,
				"byteLength": byteLength,
			},
		}
	case llm.ContentToolCall:
		if part.ToolCall == nil {
			break
		}
		value, raw := normalizeToolArguments(part.ToolCall.Arguments)
		protocolPayload := map[string]any{
			"id":        part.ToolCall.ID,
			"name":      part.ToolCall.Name,
			"arguments": value,
		}
		protocolJSON, _ := json.Marshal(protocolPayload)
		return map[string]any{
			"index":        partIndex,
			"kind":         "tool_call",
			"id":           part.ToolCall.ID,
			"name":         part.ToolCall.Name,
			"arguments":    value,
			"rawArguments": raw,
			"protocolBlock": strings.Join([]string{
				toolCallBeginMarker,
				string(protocolJSON),
				toolCallEndMarker,
			}, "\n"),
		}
	case llm.ContentToolResult:
		if part.ToolResult == nil {
			break
		}
		item := map[string]any{
			"index":      partIndex,
			"kind":       "tool_result",
			"toolCallId": part.ToolResult.ToolCallID,
			"content":    part.ToolResult.Content,
			"isError":    part.ToolResult.IsError,
		}
		if len(part.ToolResult.ImageData) > 0 {
			mediaType := strings.TrimSpace(part.ToolResult.ImageMediaType)
			if mediaType == "" {
				mediaType = "image/png"
			}
			item["imageDataUri"] = llm.DataURI(mediaType, part.ToolResult.ImageData)
			item["imageMediaType"] = mediaType
		}
		return item
	case llm.ContentThinking:
		if part.Thinking == nil {
			break
		}
		return map[string]any{
			"index":     partIndex,
			"kind":      "thinking",
			"text":      part.Thinking.Text,
			"signature": part.Thinking.Signature,
			"redacted":  false,
		}
	case llm.ContentRedThinking:
		if part.Thinking == nil {
			break
		}
		return map[string]any{
			"index":     partIndex,
			"kind":      "redacted_thinking",
			"text":      part.Thinking.Text,
			"signature": part.Thinking.Signature,
			"redacted":  true,
		}
	default:
		if kind := strings.TrimSpace(string(part.Kind)); kind != "" {
			*warnings = append(*warnings, warningForFallback(fmt.Sprintf("Custom (%s)", kind)))
			fallback := map[string]any{
				"index":        partIndex,
				"kind":         kind,
				"fallbackKind": "custom",
			}
			if part.Data != nil {
				fallback["data"] = part.Data
			}
			return fallback
		}
		*warnings = append(*warnings, llm.Warning{
			Code:    "unsupported_part",
			Message: fmt.Sprintf("Unknown content part kind at message index %d was translated to fallback text", messageIndex),
		})
		return map[string]any{
			"index":    partIndex,
			"kind":     "unknown",
			"fallback": true,
		}
	}

	*warnings = append(*warnings, llm.Warning{
		Code:    "unsupported_part",
		Message: fmt.Sprintf("Content part kind %q at message index %d was empty and translated to fallback text", part.Kind, messageIndex),
	})
	return map[string]any{
		"index":    partIndex,
		"kind":     string(part.Kind),
		"fallback": true,
	}
}

func normalizeToolArguments(arguments json.RawMessage) (any, string) {
	trimmed := strings.TrimSpace(string(arguments))
	if trimmed == "" {
		return map[string]any{}, "{}"
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(trimmed)))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return trimmed, trimmed
	}
	return parsed, trimmed
}

func warningForFallback(kind string) llm.Warning {
	return llm.Warning{
		Code:    "unsupported_part",
		Message: fmt.Sprintf("%s content parts are not natively supported by codex-app-server and were translated to deterministic transcript fallback text", kind),
	}
}

func warningForUnsupportedProviderOption(key string) llm.Warning {
	return llm.Warning{
		Code:    "unsupported_option",
		Message: fmt.Sprintf("Provider option codex_app_server.%s is not supported and was ignored", key),
	}
}

func normalizeReasoningEffort(value string, warnings *[]llm.Warning, source string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	if normalized == "" {
		return ""
	}
	if _, ok := supportedReasoningEfforts[normalized]; ok {
		return normalized
	}
	if warnings != nil {
		*warnings = append(*warnings, llm.Warning{
			Code: "unsupported_option",
			Message: fmt.Sprintf(
				"%s value %q is unsupported and was ignored (expected none, minimal, low, medium, high, or xhigh)",
				source,
				value,
			),
		})
	}
	return ""
}

func applyProviderOptions(
	request llm.Request,
	params map[string]any,
	controls *transcriptControls,
	warnings *[]llm.Warning,
) {
	options := codexProviderOptions(request.ProviderOptions)
	if len(options) == 0 {
		return
	}

	for key, value := range options {
		if turnKey, ok := turnOptionKeyMap[key]; ok {
			params[turnKey] = value
			continue
		}
		if controlKey, ok := controlOptionKeyMap[key]; ok {
			applyControlOverride(controlKey, value, controls, warnings, key)
			continue
		}
		*warnings = append(*warnings, warningForUnsupportedProviderOption(key))
	}
}

func codexProviderOptions(options map[string]any) map[string]any {
	if len(options) == 0 {
		return nil
	}
	for _, key := range []string{"codex_app_server", "codex-app-server", "codexappserver"} {
		if raw, ok := options[key]; ok {
			if m := asMap(raw); m != nil {
				return m
			}
		}
	}
	return nil
}

func applyControlOverride(
	key string,
	value any,
	controls *transcriptControls,
	warnings *[]llm.Warning,
	rawKey string,
) {
	switch key {
	case "temperature":
		if f, ok := value.(float64); ok {
			controls.Temperature = &f
			return
		}
	case "topP":
		if f, ok := value.(float64); ok {
			controls.TopP = &f
			return
		}
	case "maxTokens":
		if n, ok := value.(float64); ok {
			i := int(n)
			controls.MaxTokens = &i
			return
		}
		if n, ok := value.(int); ok {
			controls.MaxTokens = &n
			return
		}
	case "stopSequences":
		if arr, ok := value.([]any); ok {
			out := make([]string, 0, len(arr))
			for _, item := range arr {
				s := asString(item)
				if s == "" {
					*outWarning(warnings) = append(*outWarning(warnings), warningForUnsupportedProviderOption(rawKey))
					return
				}
				out = append(out, s)
			}
			controls.StopSequences = out
			return
		}
		if arr, ok := value.([]string); ok {
			controls.StopSequences = append([]string{}, arr...)
			return
		}
	case "metadata":
		if rec := asMap(value); rec != nil {
			if controls.Metadata == nil {
				controls.Metadata = map[string]interface{}{}
			}
			for mk, mv := range rec {
				controls.Metadata[mk] = mv
			}
			return
		}
	case "reasoningEffort":
		normalized := normalizeReasoningEffort(asString(value), warnings, fmt.Sprintf("codex_app_server.%s", rawKey))
		if normalized != "" {
			controls.ReasoningEff = normalized
		}
		return
	}
	*warnings = append(*warnings, warningForUnsupportedProviderOption(rawKey))
}

func outWarning(w *[]llm.Warning) *[]llm.Warning { return w }

func buildTranscript(payload transcriptPayload, choice resolvedToolChoice) (string, error) {
	toolChoiceLine := "Tool choice policy: " + choice.Mode
	if choice.Mode == "named" {
		toolChoiceLine = fmt.Sprintf("Tool choice policy: named (%s)", choice.ToolName)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	lines := []string{
		transcriptBeginMarker,
		"Stateless transcript payload for unified-llm codex-app-server translation.",
		"Treat the payload as the authoritative full conversation history.",
		"When emitting tool calls, use deterministic protocol blocks exactly:",
		toolCallBeginMarker,
		`{"id":"call_<id>","name":"<tool_name>","arguments":{}}`,
		toolCallEndMarker,
		"Do not wrap tool-call protocol blocks in markdown fences.",
		toolChoiceLine,
		transcriptPayloadBeginMarker,
		string(payloadJSON),
		transcriptPayloadEndMarker,
		transcriptEndMarker,
	}
	return strings.Join(lines, "\n"), nil
}

func isLikelyLocalPath(url string) bool {
	url = strings.TrimSpace(url)
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return false
	}
	if strings.HasPrefix(url, "data:") {
		return false
	}
	if strings.HasPrefix(url, "file://") {
		return true
	}
	if uriSchemeRE.MatchString(url) {
		return false
	}
	return true
}
