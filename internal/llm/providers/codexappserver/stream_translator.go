package codexappserver

import (
	"strconv"
	"strings"

	"github.com/danshapiro/kilroy/internal/llm"
)

const (
	toolProtocolStartToken      = "[[TOOL_CALL]]"
	toolProtocolStartTokenLower = "[[tool_call]]"
	toolProtocolEndToken        = "[[/TOOL_CALL]]"
	toolProtocolEndTokenLower   = "[[/tool_call]]"
)

const maxStartReserve = len(toolProtocolStartToken) - 1

type parsedSegment struct {
	Kind     string
	Text     string
	ToolCall *llm.ToolCallData
}

type toolProtocolStreamParser struct {
	buffer      string
	insideBlock bool
	opening     string
}

func (p *toolProtocolStreamParser) feed(delta string) []parsedSegment {
	p.buffer += delta
	return p.drain(false)
}

func (p *toolProtocolStreamParser) flush() []parsedSegment {
	return p.drain(true)
}

func (p *toolProtocolStreamParser) drain(finalize bool) []parsedSegment {
	segments := make([]parsedSegment, 0, 4)

	for {
		if p.insideBlock {
			endIdx := strings.Index(strings.ToLower(p.buffer), toolProtocolEndTokenLower)
			if endIdx < 0 {
				if !finalize {
					break
				}
				if p.buffer != "" || p.opening != "" {
					segments = append(segments, parsedSegment{Kind: "text", Text: p.opening + p.buffer})
				}
				p.buffer = ""
				p.insideBlock = false
				p.opening = ""
				continue
			}

			payload := p.buffer[:endIdx]
			closing := p.buffer[endIdx : endIdx+len(toolProtocolEndToken)]
			p.buffer = p.buffer[endIdx+len(toolProtocolEndToken):]
			p.insideBlock = false

			if toolCall := parseToolCall(payload); toolCall != nil {
				segments = append(segments, parsedSegment{Kind: "tool_call", ToolCall: toolCall})
			} else {
				segments = append(segments, parsedSegment{Kind: "text", Text: p.opening + payload + closing})
			}
			p.opening = ""
			continue
		}

		lower := strings.ToLower(p.buffer)
		startIdx := strings.Index(lower, toolProtocolStartTokenLower)
		if startIdx < 0 {
			if p.buffer == "" {
				break
			}
			if finalize {
				segments = append(segments, parsedSegment{Kind: "text", Text: p.buffer})
				p.buffer = ""
				break
			}
			if len(p.buffer) <= maxStartReserve {
				break
			}
			safeText := p.buffer[:len(p.buffer)-maxStartReserve]
			p.buffer = p.buffer[len(p.buffer)-maxStartReserve:]
			if safeText != "" {
				segments = append(segments, parsedSegment{Kind: "text", Text: safeText})
			}
			break
		}

		if startIdx > 0 {
			segments = append(segments, parsedSegment{Kind: "text", Text: p.buffer[:startIdx]})
		}

		p.opening = p.buffer[startIdx : startIdx+len(toolProtocolStartToken)]
		p.buffer = p.buffer[startIdx+len(toolProtocolStartToken):]
		p.insideBlock = true
	}

	return segments
}

type textStreamState struct {
	TextStarted bool
	Parser      *toolProtocolStreamParser
}

func translateStream(events <-chan map[string]any) <-chan llm.StreamEvent {
	out := make(chan llm.StreamEvent, 64)
	go func() {
		defer close(out)

		streamStarted := false
		streamID := ""
		model := ""
		emittedToolCalls := false
		var latestUsage *llm.Usage

		textStates := make(map[string]*textStreamState)
		reasoningByItem := make(map[string]map[string]struct{})
		activeReasoningIDs := make(map[string]struct{})

		closeReasoningForItem := func(itemID string) []llm.StreamEvent {
			itemSet := reasoningByItem[itemID]
			if len(itemSet) == 0 {
				return nil
			}
			outEvents := make([]llm.StreamEvent, 0, len(itemSet))
			for reasoningID := range itemSet {
				if _, ok := activeReasoningIDs[reasoningID]; !ok {
					continue
				}
				delete(activeReasoningIDs, reasoningID)
				outEvents = append(outEvents, llm.StreamEvent{
					Type:        llm.StreamEventReasoningEnd,
					ReasoningID: reasoningID,
				})
			}
			delete(reasoningByItem, itemID)
			return outEvents
		}

		closeAllReasoning := func() []llm.StreamEvent {
			if len(activeReasoningIDs) == 0 {
				return nil
			}
			keys := make([]string, 0, len(activeReasoningIDs))
			for reasoningID := range activeReasoningIDs {
				keys = append(keys, reasoningID)
			}
			outEvents := make([]llm.StreamEvent, 0, len(keys))
			for _, reasoningID := range keys {
				outEvents = append(outEvents, llm.StreamEvent{
					Type:        llm.StreamEventReasoningEnd,
					ReasoningID: reasoningID,
				})
				delete(activeReasoningIDs, reasoningID)
			}
			reasoningByItem = map[string]map[string]struct{}{}
			return outEvents
		}

		ensureReasoningStarted := func(itemID, reasoningID string) []llm.StreamEvent {
			if _, ok := reasoningByItem[itemID]; !ok {
				reasoningByItem[itemID] = map[string]struct{}{}
			}
			reasoningByItem[itemID][reasoningID] = struct{}{}
			if _, ok := activeReasoningIDs[reasoningID]; ok {
				return nil
			}
			activeReasoningIDs[reasoningID] = struct{}{}
			return []llm.StreamEvent{{
				Type:        llm.StreamEventReasoningStart,
				ReasoningID: reasoningID,
			}}
		}

		emitAgentSegments := func(itemID string, segments []parsedSegment) []llm.StreamEvent {
			state := textStates[itemID]
			if state == nil {
				return nil
			}
			outEvents := make([]llm.StreamEvent, 0, len(segments)*3)
			for _, segment := range segments {
				switch segment.Kind {
				case "text":
					if segment.Text == "" {
						continue
					}
					if !state.TextStarted {
						state.TextStarted = true
						outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventTextStart, TextID: itemID})
					}
					outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventTextDelta, TextID: itemID, Delta: segment.Text})
				case "tool_call":
					if segment.ToolCall == nil {
						continue
					}
					emittedToolCalls = true
					if state.TextStarted {
						state.TextStarted = false
						outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: itemID})
					}
					call := *segment.ToolCall
					outEvents = append(outEvents,
						llm.StreamEvent{Type: llm.StreamEventToolCallStart, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: firstNonEmpty(call.Type, "function")}},
						llm.StreamEvent{Type: llm.StreamEventToolCallDelta, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: firstNonEmpty(call.Type, "function"), Arguments: call.Arguments}},
						llm.StreamEvent{Type: llm.StreamEventToolCallEnd, ToolCall: &llm.ToolCallData{ID: call.ID, Name: call.Name, Type: firstNonEmpty(call.Type, "function"), Arguments: call.Arguments}},
					)
				}
			}
			return outEvents
		}

		flushAgentState := func(itemID string) []llm.StreamEvent {
			state := textStates[itemID]
			if state == nil {
				return nil
			}
			outEvents := emitAgentSegments(itemID, state.Parser.flush())
			if state.TextStarted {
				state.TextStarted = false
				outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventTextEnd, TextID: itemID})
			}
			delete(textStates, itemID)
			return outEvents
		}

		for rawEvent := range events {
			notification, ok := normalizeNotification(rawEvent)
			if !ok {
				continue
			}
			outEvents := make([]llm.StreamEvent, 0, 6)

			switch notification.Method {
			case "turn/started":
				turn := asMap(notification.Params["turn"])
				if turnID := firstNonEmpty(asString(turn["id"]), asString(notification.Params["turnId"])); turnID != "" {
					streamID = turnID
				}
				emittedToolCalls = false
				if reroutedModel := asString(notification.Params["model"]); reroutedModel != "" {
					model = reroutedModel
				}
				if !streamStarted {
					streamStarted = true
					outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventStreamStart, ID: streamID, Model: model})
				}

			case "item/agentMessage/delta":
				itemID := asString(notification.Params["itemId"])
				if itemID == "" {
					break
				}
				if _, ok := textStates[itemID]; !ok {
					textStates[itemID] = &textStreamState{Parser: &toolProtocolStreamParser{}}
				}
				delta := asString(notification.Params["delta"])
				outEvents = append(outEvents, emitAgentSegments(itemID, textStates[itemID].Parser.feed(delta))...)

			case "item/reasoning/summaryPartAdded":
				itemID := asString(notification.Params["itemId"])
				summaryIndex := asInt(notification.Params["summaryIndex"], -1)
				if itemID == "" || summaryIndex < 0 {
					break
				}
				nextReasoningID := fmtReasoningID(itemID, "summary", summaryIndex)
				if existing := reasoningByItem[itemID]; len(existing) > 0 {
					for reasoningID := range existing {
						if !strings.HasPrefix(reasoningID, itemID+":summary:") || reasoningID == nextReasoningID {
							continue
						}
						if _, ok := activeReasoningIDs[reasoningID]; ok {
							delete(activeReasoningIDs, reasoningID)
							outEvents = append(outEvents, llm.StreamEvent{Type: llm.StreamEventReasoningEnd, ReasoningID: reasoningID})
						}
					}
				}
				outEvents = append(outEvents, ensureReasoningStarted(itemID, nextReasoningID)...)

			case "item/reasoning/summaryTextDelta":
				itemID := asString(notification.Params["itemId"])
				if itemID == "" {
					break
				}
				reasoningID := fmtReasoningID(itemID, "summary", asInt(notification.Params["summaryIndex"], 0))
				outEvents = append(outEvents, ensureReasoningStarted(itemID, reasoningID)...)
				for _, segment := range splitReasoningSegments(asString(notification.Params["delta"])) {
					if segment.Text == "" {
						continue
					}
					var redacted *bool
					if segment.Redacted {
						v := true
						redacted = &v
					}
					outEvents = append(outEvents, llm.StreamEvent{
						Type:           llm.StreamEventReasoningDelta,
						ReasoningDelta: segment.Text,
						ReasoningID:    reasoningID,
						Redacted:       redacted,
					})
				}

			case "item/reasoning/textDelta":
				itemID := asString(notification.Params["itemId"])
				if itemID == "" {
					break
				}
				reasoningID := fmtReasoningID(itemID, "content", asInt(notification.Params["contentIndex"], 0))
				outEvents = append(outEvents, ensureReasoningStarted(itemID, reasoningID)...)
				for _, segment := range splitReasoningSegments(asString(notification.Params["delta"])) {
					if segment.Text == "" {
						continue
					}
					var redacted *bool
					if segment.Redacted {
						v := true
						redacted = &v
					}
					outEvents = append(outEvents, llm.StreamEvent{
						Type:           llm.StreamEventReasoningDelta,
						ReasoningDelta: segment.Text,
						ReasoningID:    reasoningID,
						Redacted:       redacted,
					})
				}

			case "item/completed":
				item := asMap(notification.Params["item"])
				if item == nil {
					break
				}
				itemID := asString(item["id"])
				itemType := asString(item["type"])
				if itemID == "" {
					break
				}
				if itemType == "agentMessage" {
					outEvents = append(outEvents, flushAgentState(itemID)...)
					break
				}
				if itemType == "reasoning" {
					outEvents = append(outEvents, closeReasoningForItem(itemID)...)
				}

			case "thread/tokenUsage/updated":
				latestUsage = usageFromTokenUsage(asMap(notification.Params["tokenUsage"]))

			case "error":
				errorData := asMap(notification.Params["error"])
				message := firstNonEmpty(asString(errorData["message"]), "Unknown stream error")
				outEvents = append(outEvents, llm.StreamEvent{
					Type: llm.StreamEventError,
					Err:  llm.NewStreamError("codex-app-server", message),
					Raw:  notification.Params,
				})

			case "turn/completed":
				turn := asMap(notification.Params["turn"])
				if turnID := asString(turn["id"]); turnID != "" {
					streamID = turnID
				}
				for itemID := range textStates {
					outEvents = append(outEvents, flushAgentState(itemID)...)
				}
				outEvents = append(outEvents, closeAllReasoning()...)
				status := asString(turn["status"])
				if status == "failed" {
					turnError := asMap(turn["error"])
					message := firstNonEmpty(asString(turnError["message"]), "Turn failed")
					outEvents = append(outEvents, llm.StreamEvent{
						Type: llm.StreamEventError,
						Err:  llm.NewStreamError("codex-app-server", message),
						Raw:  notification.Params,
					})
				}
				if turnUsage := usageFromTokenUsage(asMap(turn["tokenUsage"])); turnUsage != nil {
					latestUsage = turnUsage
				} else if turnUsage := usageFromTokenUsage(asMap(turn["token_usage"])); turnUsage != nil {
					latestUsage = turnUsage
				}
				finish := llm.FinishReason{Reason: mapFinishReason(status, emittedToolCalls), Raw: status}
				outEvents = append(outEvents, llm.StreamEvent{
					Type:         llm.StreamEventFinish,
					FinishReason: &finish,
					Usage:        latestUsage,
					Raw:          notification.Params,
				})

			default:
				outEvents = append(outEvents, llm.StreamEvent{
					Type:      llm.StreamEventProviderEvent,
					EventType: notification.Method,
					Raw:       notification.Params,
				})
			}

			if !streamStarted && notification.Method != "turn/started" {
				hasTranslated := false
				for _, event := range outEvents {
					if event.Type != llm.StreamEventProviderEvent {
						hasTranslated = true
						break
					}
				}
				if hasTranslated {
					streamStarted = true
					start := llm.StreamEvent{Type: llm.StreamEventStreamStart, ID: streamID, Model: model}
					outEvents = append([]llm.StreamEvent{start}, outEvents...)
				}
			}

			for _, event := range outEvents {
				out <- event
			}
		}
	}()
	return out
}

func usageFromTokenUsage(tokenUsage map[string]any) *llm.Usage {
	if tokenUsage == nil {
		return nil
	}
	last := asMap(tokenUsage["last"])
	if last == nil {
		last = tokenUsage
	}
	usage := llm.Usage{
		InputTokens:  asInt(last["inputTokens"], asInt(last["input_tokens"], 0)),
		OutputTokens: asInt(last["outputTokens"], asInt(last["output_tokens"], 0)),
		TotalTokens:  asInt(last["totalTokens"], asInt(last["total_tokens"], 0)),
		Raw:          tokenUsage,
	}
	if usage.TotalTokens <= 0 {
		usage.TotalTokens = usage.InputTokens + usage.OutputTokens
	}
	reasoningTokens := asInt(last["reasoningOutputTokens"], asInt(last["reasoning_tokens"], -1))
	if reasoningTokens >= 0 {
		usage.ReasoningTokens = intPtr(reasoningTokens)
	}
	cacheReadTokens := asInt(last["cachedInputTokens"], asInt(last["cache_read_input_tokens"], -1))
	if cacheReadTokens >= 0 {
		usage.CacheReadTokens = intPtr(cacheReadTokens)
	}
	cacheWriteTokens := asInt(last["cacheWriteTokens"], asInt(last["cache_write_input_tokens"], -1))
	if cacheWriteTokens >= 0 {
		usage.CacheWriteTokens = intPtr(cacheWriteTokens)
	}
	return &usage
}

func normalizeNotification(rawEvent map[string]any) (normalizedNotification, bool) {
	if method := strings.TrimSpace(asString(rawEvent["method"])); method != "" {
		params := asMap(rawEvent["params"])
		if params == nil {
			params = map[string]any{}
		}
		return normalizedNotification{Method: method, Params: params}, true
	}

	if event := strings.TrimSpace(asString(rawEvent["event"])); event != "" {
		params := map[string]any{}
		switch data := rawEvent["data"].(type) {
		case string:
			if parsed, ok := parseJSONRecord(data); ok {
				params = parsed
			}
		default:
			if rec := asMap(data); rec != nil {
				params = rec
			}
		}
		return normalizedNotification{Method: event, Params: params}, true
	}

	typ := strings.TrimSpace(asString(rawEvent["type"]))
	if strings.Contains(typ, "/") {
		params := deepCopyMap(rawEvent)
		delete(params, "type")
		return normalizedNotification{Method: typ, Params: params}, true
	}

	return normalizedNotification{}, false
}

func fmtReasoningID(itemID, segment string, idx int) string {
	return itemID + ":" + segment + ":" + strconv.Itoa(idx)
}
