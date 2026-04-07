// OpenCode CLI conversation log locator and parser.
// OpenCode stores session data in its own format — this parses tool calls and text output.
package agentlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenCodeLogLocator finds OpenCode CLI conversation log files.
type OpenCodeLogLocator struct{}

// FindLog locates the most recently modified OpenCode log file.
func (l *OpenCodeLogLocator) FindLog(workDir string, startedAfter time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}

	// OpenCode stores logs under ~/.opencode/sessions/.
	sessDir := filepath.Join(home, ".opencode", "sessions")
	return findNewestJSONL(sessDir, startedAfter)
}

// ParseOpenCodeLog reads an OpenCode conversation log and returns structured events.
func ParseOpenCodeLog(path string) ([]AgentEvent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var events []AgentEvent
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
		role, _ := raw["role"].(string)

		switch {
		case typ == "assistant" || role == "assistant":
			events = append(events, parseOpenCodeAssistant(raw)...)
		case typ == "tool_result" || typ == "tool":
			events = append(events, parseOpenCodeToolResult(raw)...)
		}
	}
	return events, nil
}

func parseOpenCodeAssistant(raw map[string]any) []AgentEvent {
	var events []AgentEvent

	// Try content blocks.
	content, ok := raw["content"].([]any)
	if !ok {
		if msg, ok := raw["message"].(map[string]any); ok {
			content, _ = msg["content"].([]any)
		}
	}

	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		blockType, _ := block["type"].(string)
		switch blockType {
		case "text":
			text, _ := block["text"].(string)
			if text != "" {
				events = append(events, AgentEvent{
					Type:    "text",
					Message: truncate(text, 200),
					Data:    map[string]any{"text": text},
				})
			}
		case "tool_use", "tool_call":
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			events = append(events, AgentEvent{
				Type:    "tool_call",
				Tool:    name,
				Message: fmt.Sprintf("%s(%s)", name, truncate(jsonStr(input), 80)),
				Data:    map[string]any{"tool": name, "args": input},
			})
		}
	}

	// Fallback: plain text content.
	if len(events) == 0 {
		if text, ok := raw["content"].(string); ok && text != "" {
			events = append(events, AgentEvent{
				Type:    "text",
				Message: truncate(text, 200),
				Data:    map[string]any{"text": text},
			})
		}
	}

	return events
}

func parseOpenCodeToolResult(raw map[string]any) []AgentEvent {
	output := ""
	if content, ok := raw["content"].(string); ok {
		output = content
	} else if content, ok := raw["output"].(string); ok {
		output = content
	}
	if output == "" {
		return nil
	}
	return []AgentEvent{{
		Type:    "tool_result",
		Message: truncate(output, 200),
		Data:    map[string]any{"content": truncate(output, 2000)},
	}}
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// OpenCodeLogDir returns the path where OpenCode stores session logs.
func OpenCodeLogDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".opencode", "sessions")
}
