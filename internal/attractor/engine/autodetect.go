// Auto-detect available LLM providers from the environment.
// Scans for API keys and CLI binaries to populate provider config.

package engine

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/danshapiro/kilroy/internal/providerspec"
)

// DetectedProvider describes a provider found via environment scanning.
type DetectedProvider struct {
	Key     string
	Backend BackendKind
	APIKey  string
}

// DetectProviders scans the environment for known API keys and returns
// provider configurations for each detected provider. For providers with
// a CLI spec, the CLI backend is preferred when the binary is on PATH.
func DetectProviders() []DetectedProvider {
	return detectProvidersWithLookPath(exec.LookPath)
}

func detectProvidersWithLookPath(lookPath func(string) (string, error)) []DetectedProvider {
	var detected []DetectedProvider
	for key, spec := range providerspec.Builtins() {
		if spec.API == nil || spec.API.DefaultAPIKeyEnv == "" {
			continue
		}
		apiKey := strings.TrimSpace(os.Getenv(spec.API.DefaultAPIKeyEnv))
		// Google also accepts GOOGLE_API_KEY as a fallback.
		if apiKey == "" && key == "google" {
			apiKey = strings.TrimSpace(os.Getenv("GOOGLE_API_KEY"))
		}
		if apiKey == "" {
			continue
		}
		backend := BackendAPI
		if spec.CLI != nil {
			if _, err := lookPath(spec.CLI.DefaultExecutable); err == nil {
				backend = BackendCLI
			}
		}
		detected = append(detected, DetectedProvider{
			Key:     key,
			Backend: backend,
			APIKey:  apiKey,
		})
	}
	return detected
}

// ApplyDetectedProviders populates cfg.LLM.Providers from auto-detected
// providers. Only providers not already configured are added.
func ApplyDetectedProviders(cfg *RunConfigFile, detected []DetectedProvider) {
	if cfg.LLM.Providers == nil {
		cfg.LLM.Providers = map[string]ProviderConfig{}
	}
	for _, dp := range detected {
		if _, exists := cfg.LLM.Providers[dp.Key]; exists {
			continue
		}
		cfg.LLM.Providers[dp.Key] = ProviderConfig{
			Backend: dp.Backend,
		}
	}
}

// ValidateGraphProviders checks that every llm_provider referenced in the
// graph has a corresponding provider entry in the config. Returns an error
// listing missing providers with guidance on which env var to set.
func ValidateGraphProviders(dotSource []byte, cfg *RunConfigFile) error {
	g, _, err := PrepareWithOptions(dotSource, PrepareOptions{
		RepoPath: cfg.Repo.Path,
	})
	if err != nil {
		return err
	}
	reg := NewDefaultRegistry()
	var missing []string
	seen := map[string]bool{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		p := normalizeProviderKey(strings.TrimSpace(n.Attr("llm_provider", "")))
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		if _, ok := cfg.LLM.Providers[p]; ok {
			continue
		}
		envHint := ""
		if spec, ok := providerspec.Builtin(p); ok && spec.API != nil {
			envHint = spec.API.DefaultAPIKeyEnv
		}
		if envHint != "" {
			missing = append(missing, fmt.Sprintf("node requires provider %q but no API key found (set %s)", p, envHint))
		} else {
			missing = append(missing, fmt.Sprintf("node requires provider %q but it was not detected or configured", p))
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s", strings.Join(missing, "; "))
	}
	return nil
}
