// Codex CLI invocation template.
package templates

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/agents/agentlog"
)

// Codex returns an invocation template for OpenAI Codex CLI (exec mode).
func Codex() Template {
	return Template{
		Name:       "codex",
		Binary:     "codex",
		LogLocator: &agentlog.CodexLogLocator{},
		BuildArgs: func(prompt, workDir, model string) []string {
			args := []string{"exec", "--sandbox", "workspace-write", "--skip-git-repo-check", "--json"}
			if model != "" {
				args = append(args, "--model", model)
			}
			if workDir != "" {
				args = append(args, "-C", workDir)
			}
			args = append(args, prompt)
			return args
		},
		BuildEnv: func() map[string]string {
			env := map[string]string{}
			if key := os.Getenv("OPENAI_API_KEY"); key != "" {
				env["OPENAI_API_KEY"] = key
			}
			return env
		},
		PrepareSession: func(stageDir string, env map[string]string) error {
			apiKey := env["OPENAI_API_KEY"]
			if apiKey == "" {
				return nil // no key available, codex will use its own auth
			}
			// Write an isolated auth.json so codex uses the API key
			// without touching ~/.codex/.
			codexHome := filepath.Join(stageDir, ".codex")
			if err := os.MkdirAll(codexHome, 0o700); err != nil {
				return err
			}
			auth := map[string]string{"auth_mode": "apikey", "OPENAI_API_KEY": apiKey}
			data, _ := json.Marshal(auth)
			if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), data, 0o600); err != nil {
				return err
			}
			env["CODEX_HOME"] = codexHome
			return nil
		},
		PromptPrefix:    "›",
		BusyIndicators:  []string{"Working", "esc to interrupt"},
		ProcessNames:    []string{"codex", "node"},
		StructuredOutput: true,
		ExitsOnComplete:  true, // exec mode exits on completion
		StartupTimeout:  30 * time.Second,
	}
}
