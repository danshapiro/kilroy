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
		BuildArgs: func(prompt, workDir string) []string {
			return []string{
				"--auto-accept-all",
				prompt,
			}
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
