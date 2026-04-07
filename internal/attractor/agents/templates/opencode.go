// OpenCode CLI invocation template.
package templates

import (
	"os"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/agentlog"
)

// OpenCode returns an invocation template for the opencode CLI.
func OpenCode() Template {
	return Template{
		Name:       "opencode",
		Binary:     "opencode",
		LogLocator: &agentlog.OpenCodeLogLocator{},
		BuildArgs: func(prompt, workDir string) []string {
			return []string{"run", prompt}
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
				env["ANTHROPIC_API_KEY"] = key
			}
			return env
		},
		PromptPrefix:    ">",
		BusyIndicators:  []string{},
		ProcessNames:    []string{"opencode"},
		ExitsOnComplete: true,
		StartupTimeout:  15 * time.Second,
	}
}
