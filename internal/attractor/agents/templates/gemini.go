// Gemini CLI invocation template.
package templates

import (
	"os"
	"time"
)

// Gemini returns an invocation template for Google Gemini CLI.
func Gemini() Template {
	return Template{
		Name:   "gemini",
		Binary: "gemini",
		BuildArgs: func(prompt, workDir, model string) []string {
			args := []string{"--auto-accept-all"}
			if model != "" {
				args = append(args, "--model", model)
			}
			args = append(args, prompt)
			return args
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("GOOGLE_API_KEY"); key != "" {
				env["GOOGLE_API_KEY"] = key
			}
			if key := os.Getenv("GEMINI_API_KEY"); key != "" {
				env["GEMINI_API_KEY"] = key
			}
			return env
		},
		PromptPrefix:    ">",
		BusyIndicators:  []string{},
		ProcessNames:    []string{"gemini"},
		ExitsOnComplete: true,
		StartupTimeout:  15 * time.Second,
	}
}
