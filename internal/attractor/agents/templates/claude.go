// Claude Code invocation template.
package templates

import (
	"os"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/agentlog"
)

// Claude returns an invocation template for Claude Code (--bare --print mode).
func Claude() Template {
	return Template{
		Name:       "claude",
		Binary:     "claude",
		LogLocator: &agentlog.ClaudeLogLocator{},
		BuildArgs: func(prompt, workDir, model string) []string {
			args := []string{"--bare", "--dangerously-skip-permissions", "--print"}
			if model != "" {
				// Claude CLI uses dashes (claude-sonnet-4-6), not dots (claude-sonnet-4.6).
				args = append(args, "--model", strings.ReplaceAll(model, ".", "-"))
			}
			args = append(args, prompt)
			return args
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
				env["ANTHROPIC_API_KEY"] = key
			}
			return env
		},
		PromptPrefix:    "❯",
		BusyIndicators:  []string{"esc to interrupt"},
		ProcessNames:    []string{"claude", "node"},
		ExitsOnComplete: true,
		// --bare skips all interactive dialogs (trust, permissions bypass).
		StartupDialogs: nil,
		StartupTimeout: 15 * time.Second,
	}
}
