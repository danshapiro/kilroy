package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/model"
)

const (
	preflightStatusPass = "pass"
	preflightStatusWarn = "warn"
	preflightStatusFail = "fail"
)

type providerPreflightReport struct {
	GeneratedAt         string                   `json:"generated_at"`
	CompletedAt         string                   `json:"completed_at,omitempty"`
	StrictCapabilities  bool                     `json:"strict_capabilities"`
	CapabilityProbeMode string                   `json:"capability_probe_mode"`
	Checks              []providerPreflightCheck `json:"checks"`
	Summary             providerPreflightSummary `json:"summary"`
}

type providerPreflightCheck struct {
	Name     string         `json:"name"`
	Provider string         `json:"provider,omitempty"`
	Status   string         `json:"status"`
	Message  string         `json:"message"`
	Details  map[string]any `json:"details,omitempty"`
}

type providerPreflightSummary struct {
	Pass int `json:"pass"`
	Warn int `json:"warn"`
	Fail int `json:"fail"`
}

func runProviderCLIPreflight(ctx context.Context, g *model.Graph, cfg *RunConfigFile, opts RunOptions) (*providerPreflightReport, error) {
	report := &providerPreflightReport{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
		CapabilityProbeMode: capabilityProbeMode(),
	}
	defer func() {
		_ = writePreflightReport(opts.LogsRoot, report)
	}()

	providers := usedCLIProviders(g, cfg)
	if len(providers) == 0 {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_cli_presence",
			Status:  preflightStatusPass,
			Message: "no cli providers used by graph",
		})
		return report, nil
	}

	for _, provider := range providers {
		exe, _ := defaultCLIInvocation(provider, "preflight-model", opts.WorktreeDir)
		if strings.TrimSpace(exe) == "" {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_presence",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  "no cli invocation mapping for provider",
			})
			return report, fmt.Errorf("preflight: no cli invocation mapping for provider %s", provider)
		}
		resolvedPath, err := exec.LookPath(exe)
		if err != nil {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_presence",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("cli binary not found: %s", exe),
			})
			return report, fmt.Errorf("preflight: provider %s cli binary not found: %s", provider, exe)
		}
		report.addCheck(providerPreflightCheck{
			Name:     "provider_cli_presence",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  "cli binary resolved",
			Details: map[string]any{
				"executable": exe,
				"path":       resolvedPath,
			},
		})

		if report.CapabilityProbeMode == "off" {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_capabilities",
				Provider: provider,
				Status:   preflightStatusPass,
				Message:  "capability probe disabled by KILROY_PREFLIGHT_CAPABILITY_PROBES=off",
			})
			continue
		}

		output, probeErr := runProviderCapabilityProbe(ctx, provider, resolvedPath)
		if probeErr != nil {
			status := preflightStatusWarn
			if report.StrictCapabilities {
				status = preflightStatusFail
			}
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_capabilities",
				Provider: provider,
				Status:   status,
				Message:  fmt.Sprintf("capability probe failed: %v", probeErr),
			})
			if report.StrictCapabilities {
				return report, fmt.Errorf("preflight: provider %s capability probe failed: %w", provider, probeErr)
			}
			continue
		}

		missing := missingCapabilityTokens(provider, output)
		if len(missing) > 0 {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_capabilities",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("required capabilities missing: %s", strings.Join(missing, ", ")),
			})
			return report, fmt.Errorf("preflight: provider %s capability probe missing required tokens: %s", provider, strings.Join(missing, ", "))
		}

		report.addCheck(providerPreflightCheck{
			Name:     "provider_cli_capabilities",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  "required capabilities detected",
		})
	}

	return report, nil
}

func writePreflightReport(logsRoot string, report *providerPreflightReport) error {
	if report == nil {
		return nil
	}
	report.CompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	report.Summary = providerPreflightSummary{}
	for _, check := range report.Checks {
		switch check.Status {
		case preflightStatusPass:
			report.Summary.Pass++
		case preflightStatusWarn:
			report.Summary.Warn++
		case preflightStatusFail:
			report.Summary.Fail++
		}
	}
	if strings.TrimSpace(logsRoot) == "" {
		return fmt.Errorf("logs root is empty")
	}
	if err := os.MkdirAll(logsRoot, 0o755); err != nil {
		return err
	}
	return writeJSON(filepath.Join(logsRoot, "preflight_report.json"), report)
}

func capabilityProbeMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_CAPABILITY_PROBES")), "off") {
		return "off"
	}
	return "on"
}

func usedCLIProviders(g *model.Graph, cfg *RunConfigFile) []string {
	used := map[string]bool{}
	if g == nil {
		return nil
	}
	for _, n := range g.Nodes {
		if n == nil || n.Shape() != "box" {
			continue
		}
		provider := normalizeProviderKey(n.Attr("llm_provider", ""))
		if provider == "" {
			continue
		}
		if backendFor(cfg, provider) != BackendCLI {
			continue
		}
		used[provider] = true
	}
	providers := make([]string, 0, len(used))
	for provider := range used {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func runProviderCapabilityProbe(ctx context.Context, provider string, exePath string) (string, error) {
	argv := []string{"--help"}
	if normalizeProviderKey(provider) == "openai" {
		argv = []string{"exec", "--help"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, exePath, argv...)
	cmd.Stdin = strings.NewReader("")
	out, err := cmd.CombinedOutput()
	if probeCtx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("probe timed out after 3s")
	}
	if err != nil {
		return "", fmt.Errorf("probe command failed: %w", err)
	}
	help := strings.TrimSpace(string(out))
	if help == "" {
		return "", fmt.Errorf("probe output empty")
	}
	return help, nil
}

func missingCapabilityTokens(provider string, helpOutput string) []string {
	text := strings.ToLower(helpOutput)
	all := []string{}
	anyOf := [][]string{}
	switch normalizeProviderKey(provider) {
	case "anthropic":
		all = []string{"--output-format", "stream-json", "--verbose"}
	case "google":
		all = []string{"--output-format"}
		anyOf = append(anyOf, []string{"--yolo", "--approval-mode"})
	case "openai":
		all = []string{"--json", "--sandbox"}
	default:
		return nil
	}

	missing := []string{}
	for _, token := range all {
		if !strings.Contains(text, token) {
			missing = append(missing, token)
		}
	}
	for _, set := range anyOf {
		found := false
		for _, token := range set {
			if strings.Contains(text, token) {
				found = true
				break
			}
		}
		if !found {
			missing = append(missing, strings.Join(set, "|"))
		}
	}
	return missing
}

func (r *providerPreflightReport) addCheck(check providerPreflightCheck) {
	if r == nil {
		return
	}
	r.Checks = append(r.Checks, check)
}
