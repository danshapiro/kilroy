// Parser dispatch for CLI agent conversation logs.
// Maps tool names to their log parsing functions.
package agentlog

// ParseFunc parses a conversation log file and returns structured events.
type ParseFunc func(path string) ([]AgentEvent, error)

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
