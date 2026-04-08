// Package templates defines per-tool invocation templates for CLI agent sessions.
// Adding a new CLI tool means defining its template — no code changes to the session manager.
package templates

import (
	"time"
)

// Template defines how to invoke a specific CLI agent tool.
type Template struct {
	Name            string // tool name (e.g. "claude", "codex")
	Binary          string // executable name
	BuildArgs       func(prompt, workDir, model string) []string
	BuildEnv        func() map[string]string
	PrepareSession  func(stageDir string, env map[string]string) error // optional pre-session setup (e.g. write config files)
	StructuredOutput bool  // when true, command output is JSONL; handler redirects to agent_output.jsonl
	PromptPrefix    string          // prompt prefix for readiness detection
	BusyIndicators  []string        // strings indicating the agent is busy
	ProcessNames    []string        // expected process names for liveness
	ExitsOnComplete bool            // true if tool exits after finishing (e.g. --print mode)
	StartupDialogs  []StartupDialog // dialogs to dismiss at startup
	StartupTimeout  time.Duration   // max time to wait for initial readiness
	LogLocator      LogLocator      // fallback: finds the CLI tool's conversation log after execution
}

// LogLocator finds and parses the conversation log written by a CLI tool.
type LogLocator interface {
	// FindLog returns the path to the most recent conversation log
	// for an execution that started at startedAfter in the given workDir.
	FindLog(workDir string, startedAfter time.Time) (string, error)
}

// StartupDialog describes an interactive dialog that must be dismissed at startup.
type StartupDialog struct {
	DetectPatterns []string      // strings to detect in pane output
	Keys           []string      // tmux key sequences to dismiss
	DelayAfter     time.Duration // delay after dismissing
}

// BuildCommand constructs the full command string for the template.
func (t *Template) BuildCommand(prompt, workDir, model string) string {
	args := t.BuildArgs(prompt, workDir, model)
	// Simple shell-safe joining for the tmux respawn-pane command.
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, t.Binary)
	parts = append(parts, args...)
	return joinCommand(parts)
}

// joinCommand joins command parts with shell quoting where needed.
func joinCommand(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += " " + shellQuote(p)
	}
	return result
}

// shellQuote wraps a string in single quotes if it contains special characters.
func shellQuote(s string) string {
	for _, c := range s {
		if c == ' ' || c == '"' || c == '\'' || c == '\\' || c == '$' || c == '`' || c == '!' || c == '(' || c == ')' || c == '{' || c == '}' || c == '[' || c == ']' || c == '<' || c == '>' || c == '|' || c == '&' || c == ';' || c == '\n' {
			// Use $'...' quoting with escaped single quotes.
			return "'" + escapeForSingleQuote(s) + "'"
		}
	}
	return s
}

func escapeForSingleQuote(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			b = append(b, '\'', '\\', '\'', '\'')
		} else {
			b = append(b, s[i])
		}
	}
	return string(b)
}
