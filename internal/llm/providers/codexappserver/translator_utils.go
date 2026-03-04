package codexappserver

import "regexp"

type reasoningSegment struct {
	Text     string
	Redacted bool
}

var redactedReasoningRE = regexp.MustCompile(`(?is)<redacted_reasoning>([\s\S]*?)</redacted_reasoning>|\[\[REDACTED_REASONING\]\]([\s\S]*?)\[\[/REDACTED_REASONING\]\]`)

func splitReasoningSegments(text string) []reasoningSegment {
	if text == "" {
		return nil
	}
	segments := make([]reasoningSegment, 0, 4)
	cursor := 0
	matches := redactedReasoningRE.FindAllStringSubmatchIndex(text, -1)
	for _, m := range matches {
		if len(m) < 6 {
			continue
		}
		start := m[0]
		end := m[1]
		if start > cursor {
			visible := text[cursor:start]
			if visible != "" {
				segments = append(segments, reasoningSegment{Text: visible})
			}
		}
		redacted := ""
		if m[2] >= 0 && m[3] >= 0 {
			redacted = text[m[2]:m[3]]
		} else if m[4] >= 0 && m[5] >= 0 {
			redacted = text[m[4]:m[5]]
		}
		if redacted != "" {
			segments = append(segments, reasoningSegment{Text: redacted, Redacted: true})
		}
		cursor = end
	}
	if cursor < len(text) {
		tail := text[cursor:]
		if tail != "" {
			prefixRe := regexp.MustCompile(`(?is)^(?:\[REDACTED\]\s*|REDACTED:\s*)([\s\S]+)$`)
			if sm := prefixRe.FindStringSubmatch(tail); len(sm) == 2 && sm[1] != "" {
				segments = append(segments, reasoningSegment{Text: sm[1], Redacted: true})
			} else {
				segments = append(segments, reasoningSegment{Text: tail})
			}
		}
	}
	return segments
}

func mapFinishReason(rawStatus string, hasToolCalls bool) string {
	if hasToolCalls {
		return llmFinishReasonToolCalls
	}
	switch rawStatus {
	case "completed":
		return llmFinishReasonStop
	case "interrupted":
		return llmFinishReasonLength
	case "failed":
		return llmFinishReasonError
	default:
		return llmFinishReasonOther
	}
}

const (
	llmFinishReasonStop      = "stop"
	llmFinishReasonLength    = "length"
	llmFinishReasonToolCalls = "tool_calls"
	llmFinishReasonError     = "error"
	llmFinishReasonOther     = "other"
)
