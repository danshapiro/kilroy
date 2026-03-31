package codexappserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/llm"
)

type normalizedNotification struct {
	Method string
	Params map[string]any
}

type itemDeltas struct {
	agentByID            map[string]string
	reasoningSummaryByID map[string]map[int]string
	reasoningContentByID map[string]map[int]string
}

var toolProtocolRE = regexp.MustCompile(`(?is)\[\[TOOL_CALL\]\]([\s\S]*?)\[\[/TOOL_CALL\]\]`)

func translateResponse(body map[string]any) (llm.Response, error) {
	notifications := extractNotifications(body)
	turn := asMap(body["turn"])
	items := collectItems(turn, notifications)

	content := make([]llm.ContentPart, 0, 8)
	for _, item := range items {
		switch asString(item["type"]) {
		case "reasoning":
			content = append(content, translateReasoning(item)...)
		case "agentMessage":
			content = append(content, translateAgentMessage(item)...)
		}
	}
	if len(content) == 0 {
		if fallback := asString(body["text"]); fallback != "" {
			content = append(content, llm.ContentPart{Kind: llm.ContentText, Text: fallback})
		}
	}

	rawStatus := firstNonEmpty(asString(turn["status"]), asString(body["status"]))
	hasToolCalls := false
	for _, part := range content {
		if part.Kind == llm.ContentToolCall {
			hasToolCalls = true
			break
		}
	}

	usage := translateUsage(body, notifications)
	warnings := extractWarnings(body, notifications)
	response := llm.Response{
		ID:       firstNonEmpty(asString(turn["id"]), asString(body["id"])),
		Model:    extractModel(body, notifications),
		Provider: "codex-app-server",
		Message: llm.Message{
			Role:    llm.RoleAssistant,
			Content: content,
		},
		Finish: llm.FinishReason{
			Reason: mapFinishReason(rawStatus, hasToolCalls),
			Raw:    rawStatus,
		},
		Usage:    usage,
		Raw:      body,
		Warnings: warnings,
	}
	return response, nil
}

func collectItems(turn map[string]any, notifications []normalizedNotification) []map[string]any {
	deltas := collectDeltas(notifications)
	orderedIDs := make([]string, 0, 16)
	byID := make(map[string]map[string]any)

	upsert := func(item map[string]any) {
		id := strings.TrimSpace(asString(item["id"]))
		if id == "" {
			return
		}
		if _, exists := byID[id]; !exists {
			orderedIDs = append(orderedIDs, id)
		}
		byID[id] = item
	}

	for _, notification := range notifications {
		if notification.Method != "item/completed" {
			continue
		}
		item := asMap(notification.Params["item"])
		if item != nil {
			upsert(item)
		}
	}
	for _, itemRaw := range asSlice(turn["items"]) {
		item := asMap(itemRaw)
		if item != nil {
			upsert(item)
		}
	}
	for itemID, text := range deltas.agentByID {
		if _, exists := byID[itemID]; exists {
			continue
		}
		upsert(map[string]any{"id": itemID, "type": "agentMessage", "text": text})
	}
	for itemID, summaryMap := range deltas.reasoningSummaryByID {
		if _, exists := byID[itemID]; exists {
			continue
		}
		upsert(map[string]any{
			"id":      itemID,
			"type":    "reasoning",
			"summary": mapByIndex(summaryMap),
			"content": mapByIndex(deltas.reasoningContentByID[itemID]),
		})
	}
	for itemID, contentMap := range deltas.reasoningContentByID {
		if _, exists := byID[itemID]; exists {
			continue
		}
		upsert(map[string]any{
			"id":      itemID,
			"type":    "reasoning",
			"summary": mapByIndex(deltas.reasoningSummaryByID[itemID]),
			"content": mapByIndex(contentMap),
		})
	}

	out := make([]map[string]any, 0, len(orderedIDs))
	for _, id := range orderedIDs {
		if item := byID[id]; item != nil {
			out = append(out, item)
		}
	}
	return out
}

func collectDeltas(notifications []normalizedNotification) itemDeltas {
	agentByID := map[string]string{}
	reasoningSummaryByID := map[string]map[int]string{}
	reasoningContentByID := map[string]map[int]string{}

	appendByIndex := func(target map[string]map[int]string, itemID string, idx int, delta string) {
		if _, ok := target[itemID]; !ok {
			target[itemID] = map[int]string{}
		}
		target[itemID][idx] = target[itemID][idx] + delta
	}

	for _, notification := range notifications {
		switch notification.Method {
		case "item/agentMessage/delta":
			itemID := asString(notification.Params["itemId"])
			if itemID == "" {
				continue
			}
			agentByID[itemID] = agentByID[itemID] + asString(notification.Params["delta"])
		case "item/reasoning/summaryTextDelta":
			itemID := asString(notification.Params["itemId"])
			if itemID == "" {
				continue
			}
			appendByIndex(reasoningSummaryByID, itemID, asInt(notification.Params["summaryIndex"], 0), asString(notification.Params["delta"]))
		case "item/reasoning/textDelta":
			itemID := asString(notification.Params["itemId"])
			if itemID == "" {
				continue
			}
			appendByIndex(reasoningContentByID, itemID, asInt(notification.Params["contentIndex"], 0), asString(notification.Params["delta"]))
		}
	}

	return itemDeltas{
		agentByID:            agentByID,
		reasoningSummaryByID: reasoningSummaryByID,
		reasoningContentByID: reasoningContentByID,
	}
}

func mapByIndex(in map[int]string) []any {
	if len(in) == 0 {
		return nil
	}
	keys := make([]int, 0, len(in))
	for idx := range in {
		keys = append(keys, idx)
	}
	sort.Ints(keys)
	out := make([]any, 0, len(keys))
	for _, idx := range keys {
		out = append(out, in[idx])
	}
	return out
}

func translateReasoning(item map[string]any) []llm.ContentPart {
	parts := make([]llm.ContentPart, 0, 4)
	for _, source := range []any{item["summary"], item["content"]} {
		for _, chunk := range asSlice(source) {
			text := asString(chunk)
			if strings.TrimSpace(text) == "" {
				continue
			}
			parts = append(parts, splitReasoningChunk(text)...)
		}
	}
	return parts
}

func splitReasoningChunk(text string) []llm.ContentPart {
	segments := splitReasoningSegments(text)
	out := make([]llm.ContentPart, 0, len(segments))
	for _, segment := range segments {
		trimmed := strings.TrimSpace(segment.Text)
		if trimmed == "" {
			continue
		}
		thinking := &llm.ThinkingData{Text: trimmed, Redacted: segment.Redacted}
		if segment.Redacted {
			out = append(out, llm.ContentPart{Kind: llm.ContentRedThinking, Thinking: thinking})
			continue
		}
		out = append(out, llm.ContentPart{Kind: llm.ContentThinking, Thinking: thinking})
	}
	return out
}

func translateAgentMessage(item map[string]any) []llm.ContentPart {
	text := asString(item["text"])
	if text == "" {
		return nil
	}
	matches := toolProtocolRE.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return []llm.ContentPart{{Kind: llm.ContentText, Text: text}}
	}

	parts := make([]llm.ContentPart, 0, len(matches)*2+1)
	cursor := 0
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		start := m[0]
		end := m[1]
		payloadStart := m[2]
		payloadEnd := m[3]
		if start > cursor {
			prefix := text[cursor:start]
			if prefix != "" {
				parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: prefix})
			}
		}
		payload := strings.TrimSpace(text[payloadStart:payloadEnd])
		if toolCall := parseToolCall(payload); toolCall != nil {
			parts = append(parts, llm.ContentPart{Kind: llm.ContentToolCall, ToolCall: toolCall})
		} else {
			block := text[start:end]
			if block != "" {
				parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: block})
			}
		}
		cursor = end
	}
	if cursor < len(text) {
		suffix := text[cursor:]
		if suffix != "" {
			parts = append(parts, llm.ContentPart{Kind: llm.ContentText, Text: suffix})
		}
	}
	return parts
}

func parseToolCall(payload string) *llm.ToolCallData {
	if strings.TrimSpace(payload) == "" {
		return nil
	}
	m, ok := parseJSONRecord(payload)
	if !ok {
		return nil
	}
	name := strings.TrimSpace(asString(m["name"]))
	if name == "" {
		return nil
	}
	id := strings.TrimSpace(asString(m["id"]))
	if id == "" {
		id = fmt.Sprintf("call_%d", time.Now().UnixNano())
	}
	typ := strings.TrimSpace(asString(m["type"]))

	argsRaw := m["arguments"]
	arguments, rawStr := normalizeParsedArguments(argsRaw)
	_ = rawStr

	toolCall := &llm.ToolCallData{
		ID:        id,
		Name:      name,
		Arguments: arguments,
	}
	if typ != "" {
		toolCall.Type = typ
	}
	return toolCall
}

func normalizeParsedArguments(value any) (json.RawMessage, string) {
	if s, ok := value.(string); ok {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			return json.RawMessage("{}"), "{}"
		}
		if json.Valid([]byte(trimmed)) {
			return json.RawMessage(trimmed), trimmed
		}
		encoded, _ := json.Marshal(trimmed)
		return json.RawMessage(encoded), trimmed
	}
	if value == nil {
		return json.RawMessage("{}"), "{}"
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) == 0 {
		return json.RawMessage("{}"), "{}"
	}
	return json.RawMessage(b), string(b)
}

func extractModel(body map[string]any, notifications []normalizedNotification) string {
	if model := firstNonEmpty(asString(body["model"]), asString(body["modelId"]), asString(body["model_name"])); model != "" {
		return model
	}
	for idx := len(notifications) - 1; idx >= 0; idx-- {
		notification := notifications[idx]
		if notification.Method != "model/rerouted" {
			continue
		}
		if model := asString(notification.Params["toModel"]); model != "" {
			return model
		}
	}
	return ""
}

func translateUsage(body map[string]any, notifications []normalizedNotification) llm.Usage {
	var usageSource map[string]any
	var rawUsage map[string]any

	for idx := len(notifications) - 1; idx >= 0; idx-- {
		notification := notifications[idx]
		if notification.Method != "thread/tokenUsage/updated" {
			continue
		}
		tokenUsage := asMap(notification.Params["tokenUsage"])
		if tokenUsage == nil {
			continue
		}
		rawUsage = tokenUsage
		usageSource = asMap(tokenUsage["last"])
		if usageSource == nil {
			usageSource = tokenUsage
		}
		break
	}
	if usageSource == nil {
		tokenUsage := asMap(body["tokenUsage"])
		if tokenUsage != nil {
			rawUsage = tokenUsage
			usageSource = asMap(tokenUsage["last"])
			if usageSource == nil {
				usageSource = tokenUsage
			}
		}
	}
	if usageSource == nil {
		usage := asMap(body["usage"])
		if usage != nil {
			rawUsage = usage
			usageSource = usage
		}
	}

	usage := llm.Usage{
		InputTokens:  asInt(usageSource["inputTokens"], asInt(usageSource["input_tokens"], 0)),
		OutputTokens: asInt(usageSource["outputTokens"], asInt(usageSource["output_tokens"], 0)),
		TotalTokens:  asInt(usageSource["totalTokens"], asInt(usageSource["total_tokens"], 0)),
	}
	reasoningTokens := asInt(usageSource["reasoningOutputTokens"], asInt(usageSource["reasoning_tokens"], -1))
	cacheReadTokens := asInt(usageSource["cachedInputTokens"], asInt(usageSource["cache_read_input_tokens"], -1))
	cacheWriteTokens := asInt(usageSource["cacheWriteTokens"], asInt(usageSource["cache_write_input_tokens"], -1))
	if usage.TotalTokens <= 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	if reasoningTokens >= 0 {
		usage.ReasoningTokens = intPtr(reasoningTokens)
	}
	if cacheReadTokens >= 0 {
		usage.CacheReadTokens = intPtr(cacheReadTokens)
	}
	if cacheWriteTokens >= 0 {
		usage.CacheWriteTokens = intPtr(cacheWriteTokens)
	}
	if rawUsage != nil {
		usage.Raw = rawUsage
	}
	return usage
}

func extractWarnings(body map[string]any, notifications []normalizedNotification) []llm.Warning {
	warnings := make([]llm.Warning, 0, 4)
	for _, warningValue := range asSlice(body["warnings"]) {
		warning := asMap(warningValue)
		if warning == nil {
			continue
		}
		message := strings.TrimSpace(asString(warning["message"]))
		if message == "" {
			continue
		}
		code := strings.TrimSpace(asString(warning["code"]))
		warnings = append(warnings, llm.Warning{Message: message, Code: code})
	}
	for _, notification := range notifications {
		if notification.Method != "deprecationNotice" && notification.Method != "configWarning" {
			continue
		}
		message := firstNonEmpty(
			asString(notification.Params["message"]),
			asString(notification.Params["notice"]),
			asString(notification.Params["warning"]),
		)
		if message == "" {
			continue
		}
		warnings = append(warnings, llm.Warning{Message: message, Code: notification.Method})
	}
	return warnings
}

func extractNotifications(body map[string]any) []normalizedNotification {
	notifications := make([]normalizedNotification, 0, 16)
	sources := make([]any, 0)
	sources = append(sources, asSlice(body["notifications"])...)
	sources = append(sources, asSlice(body["events"])...)
	sources = append(sources, asSlice(body["rawNotifications"])...)

	for _, raw := range sources {
		entry := asMap(raw)
		if entry == nil {
			continue
		}
		method := firstNonEmpty(asString(entry["method"]), asString(entry["event"]), asString(entry["type"]))
		if method == "" {
			continue
		}
		params := asMap(entry["params"])
		if params == nil {
			if dataString, ok := entry["data"].(string); ok {
				if parsed, ok := parseJSONRecord(dataString); ok {
					params = parsed
				}
			} else {
				params = asMap(entry["data"])
			}
		}
		if params == nil {
			params = map[string]any{}
		}
		notifications = append(notifications, normalizedNotification{Method: method, Params: params})
	}

	return notifications
}

func parseJSONRecord(in string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(strings.TrimSpace(in)))
	dec.UseNumber()
	var parsed map[string]any
	if err := dec.Decode(&parsed); err != nil {
		return nil, false
	}
	if parsed == nil {
		return nil, false
	}
	return parsed, true
}

func intPtr(v int) *int { return &v }

func parseJSONAny(in string) any {
	dec := json.NewDecoder(bytes.NewReader([]byte(in)))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return nil
	}
	return parsed
}
