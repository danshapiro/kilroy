// Codex CLI conversation log locator and parser.
// Parses codex exec --json JSONL output: item.completed events with agent_message,
// command_execution, and file_change item types.
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
func (l *CodexLogLocator) FindLog(workDir string, startedAfter time.Time) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("user home dir: %w", err)
	}
	sessDir := filepath.Join(home, ".codex", "sessions")
	return findNewestJSONL(sessDir, startedAfter)
}

// ParseCodexLog reads codex exec --json JSONL output and returns structured events.
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
		item, _ := raw["item"].(map[string]any)
		if item == nil {
			continue
		}
		itemType, _ := item["type"].(string)

		switch {
		case typ == "item.completed" && itemType == "agent_message":
			text, _ := item["text"].(string)
			if text != "" {
				events = append(events, AgentEvent{
					Type:    "text",
					Message: truncate(text, 200),
					Data:    map[string]any{"text": text},
				})
			}

		case (typ == "item.completed" || typ == "item.started") && itemType == "command_execution":
			cmd, _ := item["command"].(string)
			exitCode, _ := item["exit_code"].(float64)
			output, _ := item["aggregated_output"].(string)
			status, _ := item["status"].(string)

			if typ == "item.started" {
				events = append(events, AgentEvent{
					Type:    "tool_call",
					Tool:    "command",
					Message: fmt.Sprintf("Bash(%s)", truncate(cmd, 100)),
					Data:    map[string]any{"tool": "command", "command": cmd},
				})
			} else if status == "completed" && output != "" {
				events = append(events, AgentEvent{
					Type:    "tool_result",
					Message: truncate(output, 200),
					Data: map[string]any{
						"content":   truncate(output, 2000),
						"exit_code": int(exitCode),
					},
				})
			}

		case typ == "item.completed" && itemType == "file_change":
			path, _ := item["path"].(string)
			action, _ := item["action"].(string)
			events = append(events, AgentEvent{
				Type:    "tool_call",
				Tool:    "file_change",
				Message: fmt.Sprintf("FileChange(%s: %s)", action, path),
				Data:    map[string]any{"tool": "file_change", "path": path, "action": action},
			})
		}
	}
	return events, nil
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
