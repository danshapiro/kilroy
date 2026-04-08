// Parser dispatch for CLI agent conversation logs.
// Maps tool names to their log parsing functions.
package agentlog

import (
	"encoding/json"
	"strings"
)

// ParseFunc parses a conversation log file and returns structured events.
type ParseFunc func(path string) ([]AgentEvent, error)

// LineParseFunc parses a single JSONL line and returns zero or more events.
type LineParseFunc func(raw map[string]any) []AgentEvent

// ParserForTool returns the log parser function for a given tool name, or nil.
func ParserForTool(toolName string) ParseFunc {
	switch toolName {
	case "claude":
		return ParseClaudeLog
	case "codex":
		return ParseCodexLog
	case "opencode":
		return ParseOpenCodeLog
	default:
		return nil
	}
}

// LineParserForTool returns a per-line parser for a given tool name, or nil.
func LineParserForTool(toolName string) LineParseFunc {
	switch toolName {
	case "claude":
		return ParseClaudeLine
	case "codex":
		return ParseCodexLine
	case "opencode":
		return ParseOpenCodeLine
	default:
		return nil
	}
}

// ParseJSONLine attempts to parse a single JSONL line into a map.
func ParseJSONLine(line string) (map[string]any, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(line), &raw); err != nil {
		return nil, false
	}
	return raw, true
}
