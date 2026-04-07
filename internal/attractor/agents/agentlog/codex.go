// Codex CLI conversation log locator and parser.
// Codex writes JSON logs to its own format — this parses tool calls and text output.
package agentlog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CodexLogLocator finds Codex CLI conversation log files.
type CodexLogLocator struct{}

// FindLog locates the most recently modified Codex log file.
// Codex stores logs in ~/.codex/sessions/ or similar paths.
func (l *CodexLogLocator) FindLog(workDir string, startedAfter time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}

	// Codex stores session logs under ~/.codex/sessions/.
	sessDir := filepath.Join(home, ".codex", "sessions")
	return findNewestJSONL(sessDir, startedAfter)
}

// ParseCodexLog reads a Codex conversation log and returns structured events.
// Codex uses a similar JSONL format to Claude with message/content blocks.
func ParseCodexLog(path string) ([]AgentEvent, error) {
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
		case "assistant", "response":
			events = append(events, parseCodexAssistant(raw)...)
		case "tool_result", "function_result":
			events = append(events, parseCodexToolResult(raw)...)
		}
	}
	return events, nil
}

func parseCodexAssistant(raw map[string]any) []AgentEvent {
	// Codex may use different field names, but the structure is similar.
	var events []AgentEvent

	// Try OpenAI-style response format.
	if output, ok := raw["output"].([]any); ok {
		for _, item := range output {
			block, ok := item.(map[string]any)
			if !ok {
				continue
			}
			blockType, _ := block["type"].(string)
			switch blockType {
			case "message":
				if content, ok := block["content"].([]any); ok {
					for _, c := range content {
						if cm, ok := c.(map[string]any); ok {
							if text, ok := cm["text"].(string); ok && text != "" {
								events = append(events, AgentEvent{
									Type:    "text",
									Message: truncate(text, 200),
									Data:    map[string]any{"text": text},
								})
							}
						}
					}
				}
			case "function_call":
				name, _ := block["name"].(string)
				args, _ := block["arguments"].(string)
				var input map[string]any
				_ = json.Unmarshal([]byte(args), &input)
				events = append(events, AgentEvent{
					Type:    "tool_call",
					Tool:    name,
					Message: fmt.Sprintf("%s(%s)", name, truncate(args, 80)),
					Data:    map[string]any{"tool": name, "args": input},
				})
			}
		}
	}

	// Also try Claude-style content blocks.
	if msg, ok := raw["message"].(map[string]any); ok {
		if content, ok := msg["content"].([]any); ok {
			for _, item := range content {
				block, ok := item.(map[string]any)
				if !ok {
					continue
				}
				blockType, _ := block["type"].(string)
				if blockType == "text" {
					text, _ := block["text"].(string)
					if text != "" {
						events = append(events, AgentEvent{
							Type:    "text",
							Message: truncate(text, 200),
							Data:    map[string]any{"text": text},
						})
					}
				}
			}
		}
	}

	return events
}

func parseCodexToolResult(raw map[string]any) []AgentEvent {
	output, _ := raw["output"].(string)
	if output == "" {
		return nil
	}
	return []AgentEvent{{
		Type:    "tool_result",
		Message: truncate(output, 200),
		Data:    map[string]any{"content": truncate(output, 2000)},
	}}
}

// findNewestJSONL returns the most recently modified .jsonl file in a directory.
func findNewestJSONL(dir string, after time.Time) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
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
		if !after.IsZero() && mod.Before(after) {
			continue
		}
		if best == "" || mod.After(bestMod) {
			best = filepath.Join(dir, e.Name())
			bestMod = mod
		}
	}
	if best == "" {
		return "", fmt.Errorf("no JSONL files found in %s after %s", dir, after)
	}
	return best, nil
}
