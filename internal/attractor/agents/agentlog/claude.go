// Claude JSONL conversation log locator and parser.
// Reads Claude Code's conversation logs and extracts tool_use, tool_result, text, and thinking events.
package agentlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ClaudeLogLocator finds Claude Code conversation JSONL files.
type ClaudeLogLocator struct{}

// FindLog locates the most recently modified JSONL file in Claude's project directory
// for the given working directory, filtering to files modified after startedAfter.
func (l *ClaudeLogLocator) FindLog(workDir string, startedAfter time.Time) (string, error) {
	projectDir := claudeProjectDir(workDir)
	if projectDir == "" {
		return "", fmt.Errorf("could not determine Claude project directory for %s", workDir)
	}

	entries, err := os.ReadDir(projectDir)
	if err != nil {
		return "", fmt.Errorf("read Claude project dir %s: %w", projectDir, err)
	}

	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		mod := info.ModTime()
		if !startedAfter.IsZero() && mod.Before(startedAfter) {
			continue
		}
		if best == "" || mod.After(bestMod) {
			best = filepath.Join(projectDir, e.Name())
			bestMod = mod
		}
	}

	// Also check subdirectories (subagents, etc).
	subdirs, _ := os.ReadDir(projectDir)
	for _, sd := range subdirs {
		if !sd.IsDir() {
			continue
		}
		subEntries, err := os.ReadDir(filepath.Join(projectDir, sd.Name()))
		if err != nil {
			continue
		}
		for _, e := range subEntries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			mod := info.ModTime()
			if !startedAfter.IsZero() && mod.Before(startedAfter) {
				continue
			}
			if best == "" || mod.After(bestMod) {
				best = filepath.Join(projectDir, sd.Name(), e.Name())
				bestMod = mod
			}
		}
	}

	if best == "" {
		return "", fmt.Errorf("no Claude conversation log found in %s after %s", projectDir, startedAfter)
	}
	return best, nil
}

// claudeProjectDir returns the Claude projects directory for a working directory.
// Claude encodes the path by replacing / with - and prefixing with -.
func claudeProjectDir(workDir string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	// Claude's path encoding: absolute path with / replaced by -.
	encoded := strings.ReplaceAll(workDir, "/", "-")
	return filepath.Join(home, ".claude", "projects", encoded)
}

// AgentEvent represents a parsed event from a CLI agent's conversation log.
type AgentEvent struct {
	Type    string         `json:"type"`    // tool_call, tool_result, text, thinking
	Tool    string         `json:"tool"`    // tool name (for tool_call/tool_result)
	Message string         `json:"msg"`     // human-readable summary
	Data    map[string]any `json:"data"`    // structured payload
}

// ParseClaudeLog reads a Claude JSONL conversation file and returns structured events.
func ParseClaudeLog(path string) ([]AgentEvent, error) {
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
		switch typ {
		case "assistant":
			events = append(events, parseAssistantMessage(raw)...)
		case "user":
			events = append(events, parseUserMessage(raw)...)
		}
	}
	return events, nil
}

// parseAssistantMessage extracts text, tool_use, and thinking blocks from an assistant message.
func parseAssistantMessage(raw map[string]any) []AgentEvent {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}

	var events []AgentEvent
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
		case "tool_use":
			name, _ := block["name"].(string)
			input, _ := block["input"].(map[string]any)
			events = append(events, AgentEvent{
				Type:    "tool_call",
				Tool:    name,
				Message: formatToolCall(name, input),
				Data: map[string]any{
					"tool": name,
					"args": input,
				},
			})
		case "thinking":
			text, _ := block["thinking"].(string)
			if text != "" {
				events = append(events, AgentEvent{
					Type:    "thinking",
					Message: truncate(text, 200),
				})
			}
		}
	}
	return events
}

// parseUserMessage extracts tool_result blocks from a user message.
func parseUserMessage(raw map[string]any) []AgentEvent {
	msg, ok := raw["message"].(map[string]any)
	if !ok {
		return nil
	}
	content, ok := msg["content"].([]any)
	if !ok {
		return nil
	}

	var events []AgentEvent
	for _, item := range content {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if blockType, _ := block["type"].(string); blockType == "tool_result" {
			toolUseID, _ := block["tool_use_id"].(string)
			contentStr := extractToolResultContent(block)
			events = append(events, AgentEvent{
				Type:    "tool_result",
				Message: truncate(contentStr, 200),
				Data: map[string]any{
					"tool_use_id": toolUseID,
					"content":     truncate(contentStr, 2000),
				},
			})
		}
	}
	return events
}

// extractToolResultContent extracts the content string from a tool_result block.
func extractToolResultContent(block map[string]any) string {
	content := block["content"]
	switch v := content.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if m, ok := item.(map[string]any); ok {
				if text, ok := m["text"].(string); ok {
					parts = append(parts, text)
				}
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// formatToolCall creates a human-readable summary of a tool call.
func formatToolCall(name string, input map[string]any) string {
	switch name {
	case "Read":
		if path, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Read(%s)", path)
		}
	case "Write":
		if path, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Write(%s)", path)
		}
	case "Edit":
		if path, ok := input["file_path"].(string); ok {
			return fmt.Sprintf("Edit(%s)", path)
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			return fmt.Sprintf("Bash(%s)", truncate(cmd, 80))
		}
	case "Glob":
		if pattern, ok := input["pattern"].(string); ok {
			return fmt.Sprintf("Glob(%s)", pattern)
		}
	case "Grep":
		if pattern, ok := input["pattern"].(string); ok {
			return fmt.Sprintf("Grep(%s)", pattern)
		}
	}
	b, _ := json.Marshal(input)
	return fmt.Sprintf("%s(%s)", name, truncate(string(b), 80))
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
