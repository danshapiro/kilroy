package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/providerspec"
)

const (
	preflightStatusPass = "pass"
	preflightStatusWarn = "warn"
	preflightStatusFail = "fail"
)

type providerPreflightReport struct {
	GeneratedAt         string                   `json:"generated_at"`
	CompletedAt         string                   `json:"completed_at,omitempty"`
	CLIProfile          string                   `json:"cli_profile,omitempty"`
	AllowTestShim       bool                     `json:"allow_test_shim"`
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

func runProviderCLIPreflight(ctx context.Context, g *model.Graph, runtimes map[string]ProviderRuntime, cfg *RunConfigFile, opts RunOptions) (*providerPreflightReport, error) {
	report := &providerPreflightReport{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CLIProfile:          normalizedCLIProfile(cfg),
		AllowTestShim:       opts.AllowTestShim,
		StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
		CapabilityProbeMode: capabilityProbeMode(),
	}
	strictModelProbes := parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_MODEL_PROBES")), false)
	defer func() {
		_ = writePreflightReport(opts.LogsRoot, report)
	}()

	providers := usedCLIProviders(g, runtimes)
	if len(providers) == 0 {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_cli_presence",
			Status:  preflightStatusPass,
			Message: "no cli providers used by graph",
		})
		return report, nil
	}

	for _, provider := range providers {
		execResolution, err := resolveProviderExecutable(cfg, provider, opts)
		if err != nil {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_presence",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  err.Error(),
			})
			return report, fmt.Errorf("preflight: provider %s executable policy rejected run: %w", provider, err)
		}
		exe := execResolution.Executable
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
				"source":     execResolution.Source,
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
		if !probeOutputLooksLikeHelp(provider, output) {
			status := preflightStatusWarn
			if report.StrictCapabilities {
				status = preflightStatusFail
			}
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_capabilities",
				Provider: provider,
				Status:   status,
				Message:  "capability probe output was not recognizable help text",
			})
			if report.StrictCapabilities {
				return report, fmt.Errorf("preflight: provider %s capability probe output not parseable as help", provider)
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
		if normalizeProviderKey(provider) != "google" {
			continue
		}

		models := usedCLIModelsForProvider(g, runtimes, provider, opts)
		if len(models) == 0 {
			continue
		}
		if modelProbeMode() == "off" {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_model_access",
				Provider: provider,
				Status:   preflightStatusPass,
				Message:  "model access probe disabled by KILROY_PREFLIGHT_MODEL_PROBES=off",
			})
			continue
		}
		for _, modelID := range models {
			output, probeErr := runProviderModelAccessProbe(ctx, provider, resolvedPath, modelID)
			if probeErr == nil {
				report.addCheck(providerPreflightCheck{
					Name:     "provider_cli_model_access",
					Provider: provider,
					Status:   preflightStatusPass,
					Message:  fmt.Sprintf("model %s accepted by provider cli", modelID),
				})
				continue
			}

			combined := strings.ToLower(strings.TrimSpace(output + "\n" + probeErr.Error()))
			if normalizeProviderKey(provider) == "google" && isGoogleModelNotFound(combined) {
				report.addCheck(providerPreflightCheck{
					Name:     "provider_cli_model_access",
					Provider: provider,
					Status:   preflightStatusFail,
					Message:  fmt.Sprintf("model %s not available to configured account/endpoint", modelID),
				})
				return report, fmt.Errorf("preflight: provider %s model probe failed for model %s: model not available", provider, modelID)
			}

			status := preflightStatusWarn
			if strictModelProbes {
				status = preflightStatusFail
			}
			report.addCheck(providerPreflightCheck{
				Name:     "provider_cli_model_access",
				Provider: provider,
				Status:   status,
				Message:  fmt.Sprintf("model %s probe failed: %v", modelID, probeErr),
			})
			if strictModelProbes {
				return report, fmt.Errorf("preflight: provider %s model probe failed for model %s: %w", provider, modelID, probeErr)
			}
		}
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

func modelProbeMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_MODEL_PROBES")), "off") {
		return "off"
	}
	return "on"
}

func usedCLIProviders(g *model.Graph, runtimes map[string]ProviderRuntime) []string {
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
		rt, ok := runtimes[provider]
		if !ok || rt.Backend != BackendCLI {
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

func usedCLIModelsForProvider(g *model.Graph, runtimes map[string]ProviderRuntime, provider string, opts RunOptions) []string {
	provider = normalizeProviderKey(provider)
	if provider == "" || g == nil {
		return nil
	}
	if forcedModel, forced := forceModelForProvider(opts.ForceModels, provider); forced {
		return []string{forcedModel}
	}
	seen := map[string]bool{}
	models := []string{}
	for _, n := range g.Nodes {
		if n == nil || n.Shape() != "box" {
			continue
		}
		nodeProvider := normalizeProviderKey(n.Attr("llm_provider", ""))
		if nodeProvider == "" || nodeProvider != provider {
			continue
		}
		rt, ok := runtimes[nodeProvider]
		if !ok || rt.Backend != BackendCLI {
			continue
		}
		modelID := modelIDForNode(n)
		if modelID == "" || seen[modelID] {
			continue
		}
		seen[modelID] = true
		models = append(models, modelID)
	}
	sort.Strings(models)
	return models
}

func runProviderModelAccessProbe(ctx context.Context, provider string, exePath string, modelID string) (string, error) {
	if normalizeProviderKey(provider) != "google" {
		return "", nil
	}
	args := []string{"-p", "--output-format", "stream-json", "--yolo", "--model", modelID}
	args = insertPromptArg(args, "respond with OK")
	return runProviderProbe(ctx, exePath, args, 12*time.Second)
}

func runProviderCapabilityProbe(ctx context.Context, provider string, exePath string) (string, error) {
	argv := []string{"--help"}
	if spec := defaultCLISpecForProvider(provider); spec != nil && len(spec.HelpProbeArgs) > 0 {
		argv = append([]string{}, spec.HelpProbeArgs...)
	}
	help, err := runProviderProbe(ctx, exePath, argv, 3*time.Second)
	if err != nil {
		return "", err
	}
	if help == "" {
		return "", fmt.Errorf("probe output empty")
	}
	return help, nil
}

func runProviderProbe(ctx context.Context, exePath string, argv []string, timeout time.Duration) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, exePath, argv...)
	cmd.Stdin = strings.NewReader("")
	cmd.Env = scrubPreflightProbeEnv(os.Environ())
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("probe command failed: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	cleanup := func() {
		_ = killProcessGroup(cmd, syscall.SIGTERM)
		select {
		case <-waitCh:
			return
		case <-time.After(250 * time.Millisecond):
		}
		_ = killProcessGroup(cmd, syscall.SIGKILL)
		select {
		case <-waitCh:
		case <-time.After(2 * time.Second):
		}
	}

	select {
	case err := <-waitCh:
		output := strings.TrimSpace(out.String())
		if err != nil {
			return output, fmt.Errorf("probe command failed: %w", err)
		}
		return output, nil
	case <-probeCtx.Done():
		cleanup()
		output := strings.TrimSpace(out.String())
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			return output, fmt.Errorf("probe timed out after %s", timeout)
		}
		return output, fmt.Errorf("probe canceled: %w", probeCtx.Err())
	}
}

func missingCapabilityTokens(provider string, helpOutput string) []string {
	return missingCapabilityTokensFromSpec(defaultCLISpecForProvider(provider), helpOutput)
}

func missingCapabilityTokensFromSpec(spec *providerspec.CLISpec, helpOutput string) []string {
	if spec == nil {
		return nil
	}
	text := strings.ToLower(helpOutput)
	all := append([]string{}, spec.CapabilityAll...)
	anyOf := append([][]string{}, spec.CapabilityAnyOf...)
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

func probeOutputLooksLikeHelp(provider string, output string) bool {
	return probeOutputLooksLikeHelpFromSpec(defaultCLISpecForProvider(provider), output)
}

func probeOutputLooksLikeHelpFromSpec(spec *providerspec.CLISpec, output string) bool {
	text := strings.ToLower(strings.TrimSpace(output))
	if text == "" {
		return false
	}
	if spec == nil || len(spec.CapabilityAll) == 0 {
		return strings.Contains(text, "usage")
	}
	for _, token := range spec.CapabilityAll {
		if strings.Contains(text, strings.ToLower(token)) {
			return true
		}
	}
	return strings.Contains(text, "usage")
}

func scrubPreflightProbeEnv(base []string) []string {
	if len(base) == 0 {
		return nil
	}
	out := make([]string, 0, len(base))
	for _, entry := range base {
		key := entry
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			key = entry[:idx]
		}
		if strings.HasPrefix(key, "KILROY_TEST_") ||
			strings.HasPrefix(key, "KILROY_WATCHDOG_") ||
			strings.HasPrefix(key, "KILROY_CANCEL_") ||
			key == "KILROY_CALL_COUNT_FILE" {
			continue
		}
		out = append(out, entry)
	}
	return out
}

func (r *providerPreflightReport) addCheck(check providerPreflightCheck) {
	if r == nil {
		return
	}
	r.Checks = append(r.Checks, check)
}
