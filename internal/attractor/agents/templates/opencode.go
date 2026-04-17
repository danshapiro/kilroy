// OpenCode CLI invocation template.
package templates

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/agentlog"
)

// OpenCode returns an invocation template for the opencode CLI.
func OpenCode() Template {
	return Template{
		Name:       "opencode",
		Binary:     "opencode",
		LogLocator: &agentlog.OpenCodeLogLocator{},
		BuildArgs: func(prompt, workDir, model string) []string {
			args := []string{"run", "--format", "json", "--pure"}
			if model != "" {
				// opencode uses provider/model format (e.g. "anthropic/claude-sonnet-4-5").
				// Add "anthropic/" prefix if missing, normalize dots to dashes.
				m := strings.ReplaceAll(model, ".", "-")
				if !strings.Contains(m, "/") {
					m = "anthropic/" + m
				}
				args = append(args, "--model", m)
			}
			if workDir != "" {
				args = append(args, "--dir", workDir)
			}
			args = append(args, prompt)
			return args
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
				env["ANTHROPIC_API_KEY"] = key
			}
			if key := os.Getenv("OPENAI_API_KEY"); key != "" {
				env["OPENAI_API_KEY"] = key
			}
			return env
		},
		PrepareSession: func(stageDir string, env map[string]string) error {
			// Inject provider config via OPENCODE_CONFIG_CONTENT so opencode
			// doesn't rely on ~/.config/opencode/ or interactive auth.
			config := map[string]any{
				"provider": map[string]any{
					"anthropic": map[string]any{
						"options": map[string]any{
							"apiKey": "{env:ANTHROPIC_API_KEY}",
						},
					},
				},
			}
			data, _ := json.Marshal(config)
			env["OPENCODE_CONFIG_CONTENT"] = string(data)
			return nil
		},
		PromptPrefix:    ">",
		BusyIndicators:  []string{},
		ProcessNames:    []string{"opencode"},
		StructuredOutput: true,
		ExitsOnComplete:  true,
		StartupTimeout:  15 * time.Second,
	}
}
