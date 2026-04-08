// OpenCode CLI conversation log locator and parser.
// Parses opencode run --format json JSONL output: tool_use, text, step_start/finish events.
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
	sessDir := filepath.Join(home, ".opencode", "sessions")
	return findNewestJSONL(sessDir, startedAfter)
}

// ParseOpenCodeLog reads opencode run --format json JSONL output and returns structured events.
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
		part, _ := raw["part"].(map[string]any)

		switch typ {
		case "tool_use":
			if part == nil {
				continue
			}
			tool, _ := part["tool"].(string)
			state, _ := part["state"].(map[string]any)
			if state == nil {
				continue
			}
			input, _ := state["input"].(map[string]any)
			output, _ := state["output"].(string)
			status, _ := state["status"].(string)
			title, _ := state["title"].(string)

			// Emit tool_call.
			msg := formatOpenCodeToolCall(tool, input, title)
			events = append(events, AgentEvent{
				Type:    "tool_call",
				Tool:    tool,
				Message: msg,
				Data:    map[string]any{"tool": tool, "args": input},
			})

			// Emit tool_result if completed with output.
			if status == "completed" && output != "" {
				events = append(events, AgentEvent{
					Type:    "tool_result",
					Message: truncate(output, 200),
					Data:    map[string]any{"content": truncate(output, 2000)},
				})
			}

		case "text":
			if part == nil {
				continue
			}
			// part.type == "text", content in part.content or raw text field.
			text := ""
			if content, ok := part["content"].(string); ok {
				text = content
			}
			if text == "" {
				if t, ok := raw["text"].(string); ok {
					text = t
				}
			}
			if text != "" {
				events = append(events, AgentEvent{
					Type:    "text",
					Message: truncate(text, 200),
					Data:    map[string]any{"text": text},
				})
			}
		}
	}
	return events, nil
}

func formatOpenCodeToolCall(tool string, input map[string]any, title string) string {
	if title != "" {
		return fmt.Sprintf("%s(%s)", tool, truncate(title, 100))
	}
	switch tool {
	case "read":
		if path, ok := input["filePath"].(string); ok {
			return fmt.Sprintf("Read(%s)", path)
		}
	case "write":
		if path, ok := input["filePath"].(string); ok {
			return fmt.Sprintf("Write(%s)", path)
		}
	case "bash":
		if cmd, ok := input["command"].(string); ok {
			return fmt.Sprintf("Bash(%s)", truncate(cmd, 100))
		}
	}
	b, _ := json.Marshal(input)
	return fmt.Sprintf("%s(%s)", tool, truncate(string(b), 80))
}

func jsonStr(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
