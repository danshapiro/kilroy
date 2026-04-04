// Claude Code invocation template.
package templates

import (
	"os"
	"time"
)

// Claude returns an invocation template for Claude Code (--print mode).
func Claude() Template {
	return Template{
		Name:   "claude",
		Binary: "claude",
		BuildArgs: func(prompt, workDir string) []string {
			return []string{
				"--dangerously-skip-permissions",
				"--print",
				prompt,
			}
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
		StartupDialogs: []StartupDialog{
			{
				DetectPatterns: []string{"trust this folder", "Quick safety check", "Do you trust the contents"},
				Keys:           []string{"Enter"},
				DelayAfter:     500 * time.Millisecond,
			},
			{
				DetectPatterns: []string{"Bypass Permissions mode"},
				Keys:           []string{"Down", "Enter"},
				DelayAfter:     500 * time.Millisecond,
			},
		},
		StartupTimeout: 30 * time.Second,
	}
}
