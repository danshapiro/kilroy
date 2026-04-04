// Codex CLI invocation template.
package templates

import (
	"os"
	"time"
)

// Codex returns an invocation template for OpenAI Codex CLI.
func Codex() Template {
	return Template{
		Name:   "codex",
		Binary: "codex",
		BuildArgs: func(prompt, workDir string) []string {
			return []string{
				"--full-auto",
				prompt,
			}
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("OPENAI_API_KEY"); key != "" {
				env["OPENAI_API_KEY"] = key
			}
			return env
		},
		PromptPrefix:    "›",
		BusyIndicators:  []string{"Working", "esc to interrupt"},
		ProcessNames:    []string{"codex", "node"},
		ExitsOnComplete: false,
		StartupTimeout:  30 * time.Second,
		StartupDialogs: []StartupDialog{
			{DetectPatterns: []string{"trust the contents"}, Keys: []string{"Enter"}},
		},
	}
}
