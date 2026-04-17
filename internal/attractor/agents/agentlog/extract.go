// Extract human-readable response text from structured JSONL output.
// Each CLI tool has its own JSONL format; this normalizes to plain text.
package agentlog

import (
	"encoding/json"
	"strings"
)

// ExtractResponseText extracts the final text response from structured JSONL output.
func ExtractResponseText(tool string, data []byte) string {
	switch tool {
	case "claude":
		return extractClaudeResponseText(data)
	case "codex":
		return extractCodexResponseText(data)
	case "opencode":
		return extractOpenCodeResponseText(data)
	default:
		return ""
	}
}

func extractClaudeResponseText(data []byte) string {
	var texts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		typ, _ := raw["type"].(string)
		if typ == "result" {
			if result, ok := raw["result"].(string); ok {
				return result
			}
		}
		if typ == "assistant" {
			msg, _ := raw["message"].(map[string]any)
			content, _ := msg["content"].([]any)
			for _, item := range content {
				block, _ := item.(map[string]any)
				if blockType, _ := block["type"].(string); blockType == "text" {
					if text, ok := block["text"].(string); ok && text != "" {
						texts = append(texts, text)
					}
				}
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

func extractCodexResponseText(data []byte) string {
	var texts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		typ, _ := raw["type"].(string)
		if typ == "message" {
			if content, ok := raw["content"].(string); ok && content != "" {
				texts = append(texts, content)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}

func extractOpenCodeResponseText(data []byte) string {
	var texts []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		typ, _ := raw["type"].(string)
		if typ == "text" {
			if text, ok := raw["text"].(string); ok && text != "" {
				texts = append(texts, text)
			}
		}
	}
	return strings.Join(texts, "\n\n")
}
