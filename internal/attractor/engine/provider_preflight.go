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
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/llm"
	"github.com/danshapiro/kilroy/internal/providerspec"
)

const (
	preflightStatusPass = "pass"
	preflightStatusWarn = "warn"
	preflightStatusFail = "fail"

	preflightAPIPromptProbeTransportComplete = "complete"
	preflightAPIPromptProbeTransportStream   = "stream"

	defaultPreflightAPIPromptProbeTimeout   = 30 * time.Second
	defaultPreflightAPIPromptProbeRetries   = 2
	defaultPreflightAPIPromptProbeBaseDelay = 500 * time.Millisecond
	defaultPreflightAPIPromptProbeMaxDelay  = 5 * time.Second
)

type providerPreflightReport struct {
	GeneratedAt         string                   `json:"generated_at"`
	CompletedAt         string                   `json:"completed_at,omitempty"`
	CLIProfile          string                   `json:"cli_profile,omitempty"`
	AllowTestShim       bool                     `json:"allow_test_shim"`
	StrictCapabilities  bool                     `json:"strict_capabilities"`
	CapabilityProbeMode string                   `json:"capability_probe_mode"`
	PromptProbeMode     string                   `json:"prompt_probe_mode"`
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

type preflightAPIPromptProbeTarget struct {
	Provider  string
	Model     string
	Mode      string
	Transport string
	Request   llm.Request
}

type preflightAPIPromptProbeResult struct {
	Text       string
	Transport  string
	MaxTokens  int
	PolicyHint string
}

func runProviderCLIPreflight(ctx context.Context, g *model.Graph, runtimes map[string]ProviderRuntime, cfg *RunConfigFile, opts RunOptions, catalog *modeldb.Catalog, catalogChecks []providerPreflightCheck) (*providerPreflightReport, error) {
	report := &providerPreflightReport{
		GeneratedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		CLIProfile:          normalizedCLIProfile(cfg),
		AllowTestShim:       opts.AllowTestShim,
		StrictCapabilities:  parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_CAPABILITIES")), false),
		CapabilityProbeMode: capabilityProbeMode(),
		PromptProbeMode:     promptProbeMode(cfg),
	}
	for _, c := range catalogChecks {
		report.addCheck(c)
	}
	defer func() {
		_ = writePreflightReport(opts.LogsRoot, report)
	}()

	// Validate CLI-only models: fail early if a CLI-only model (e.g.,
	// gpt-5.3-codex-spark) is used but its provider is not configured with
	// backend=cli.
	if err := validateCLIOnlyModels(g, runtimes, opts.ForceModels, report); err != nil {
		return report, err
	}

	if err := runProviderAPIPreflight(ctx, g, runtimes, cfg, opts, report, catalog); err != nil {
		return report, err
	}

	strictModelProbes := parseBool(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_STRICT_MODEL_PROBES")), false)
	if err := runProviderCLIPreflightChecks(ctx, g, runtimes, cfg, opts, report, strictModelProbes); err != nil {
		return report, err
	}
	return report, nil
}

func validateCLIOnlyModels(g *model.Graph, runtimes map[string]ProviderRuntime, forceModels map[string]string, report *providerPreflightReport) error {
	if g == nil {
		return nil
	}
	reg := NewDefaultRegistry()
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		provider := normalizeProviderKey(n.Attr("llm_provider", ""))
		modelID := strings.TrimSpace(n.Attr("llm_model", ""))
		if modelID == "" {
			modelID = strings.TrimSpace(n.Attr("model", ""))
		}
		// When force-model is active for this provider, the graph-declared
		// model is replaced at runtime. Validate the forced model instead.
		if forcedID, forced := forceModelForProvider(forceModels, provider); forced {
			modelID = forcedID
		}
		if !isCLIOnlyModel(modelID) {
			continue
		}
		rt, ok := runtimes[provider]
		if !ok || rt.Backend != BackendCLI {
			configuredBackend := BackendKind("none")
			if ok {
				configuredBackend = rt.Backend
			}
			report.addCheck(providerPreflightCheck{
				Name:     "cli_only_model_backend",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("model %s is CLI-only (no API) but provider %s is configured with backend=%s; set backend=cli in run config", modelID, provider, configuredBackend),
			})
			return fmt.Errorf("preflight: model %s is CLI-only but provider %s has backend=%s (requires backend=cli)", modelID, provider, configuredBackend)
		}
		report.addCheck(providerPreflightCheck{
			Name:     "cli_only_model_backend",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  fmt.Sprintf("CLI-only model %s: provider backend=cli confirmed", modelID),
		})
	}
	return nil
}

func runProviderAPIPreflight(ctx context.Context, g *model.Graph, runtimes map[string]ProviderRuntime, cfg *RunConfigFile, opts RunOptions, report *providerPreflightReport, catalog *modeldb.Catalog) error {
	providers := usedAPIProviders(g, runtimes)
	if len(providers) == 0 {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_api_presence",
			Status:  preflightStatusPass,
			Message: "no api providers used by graph",
		})
		return nil
	}

	for _, provider := range providers {
		rt, ok := runtimes[provider]
		if !ok {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_api_credentials",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  "provider runtime definition missing",
			})
			return fmt.Errorf("preflight: provider %s missing runtime definition", provider)
		}
		keyEnv := strings.TrimSpace(rt.API.DefaultAPIKeyEnv)
		if keyEnv == "" {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_api_credentials",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  "api key env is not configured",
			})
			return fmt.Errorf("preflight: provider %s api key env is not configured", provider)
		}
		if strings.TrimSpace(os.Getenv(keyEnv)) == "" {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_api_credentials",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("required api key env %s is not set", keyEnv),
			})
			return fmt.Errorf("preflight: provider %s missing api key env %s", provider, keyEnv)
		}
		report.addCheck(providerPreflightCheck{
			Name:     "provider_api_credentials",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  "api key env detected",
			Details: map[string]any{
				"api_key_env": keyEnv,
			},
		})
	}

	if report.PromptProbeMode == "off" {
		for _, provider := range providers {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_prompt_probe",
				Provider: provider,
				Status:   preflightStatusPass,
				Message:  "prompt probe disabled by config/env policy",
				Details: map[string]any{
					"backend": "api",
				},
			})
		}
		return nil
	}

	client, err := newAPIClientFromProviderRuntimes(runtimes)
	if err != nil {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_api_client",
			Status:  preflightStatusFail,
			Message: fmt.Sprintf("api client initialization failed: %v", err),
		})
		return fmt.Errorf("preflight: api client initialization failed: %w", err)
	}

	available := map[string]bool{}
	for _, provider := range client.ProviderNames() {
		available[normalizeProviderKey(provider)] = true
	}
	transports, explicitTransports, err := configuredAPIPromptProbeTransports(cfg)
	if err != nil {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_prompt_probe_transports",
			Status:  preflightStatusFail,
			Message: err.Error(),
		})
		return fmt.Errorf("preflight: %w", err)
	}
	policy := preflightAPIPromptProbePolicyFromConfig(cfg)

	for _, provider := range providers {
		if !available[provider] {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_api_client",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  "provider adapter not available in api client",
			})
			return fmt.Errorf("preflight: provider %s api adapter is not available", provider)
		}

		targets, targetErr := usedAPIPromptProbeTargetsForProvider(g, runtimes, provider, opts, transports, catalog)
		if targetErr != nil {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_prompt_probe",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("unable to build prompt probe targets: %v", targetErr),
				Details: map[string]any{
					"backend": "api",
				},
			})
			return fmt.Errorf("preflight: provider %s unable to build prompt probe targets: %w", provider, targetErr)
		}
		if len(targets) == 0 {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_prompt_probe",
				Provider: provider,
				Status:   preflightStatusPass,
				Message:  "no prompt probe targets used by graph for api provider",
				Details: map[string]any{
					"backend": "api",
				},
			})
			continue
		}
		for _, target := range targets {
			probe, probeErr := runProviderAPIPromptProbeTargetWithPolicy(ctx, client, target, policy)
			effectiveTransport := strings.TrimSpace(probe.Transport)
			if effectiveTransport == "" {
				effectiveTransport = strings.TrimSpace(target.Transport)
			}
			if effectiveTransport == "" {
				effectiveTransport = preflightAPIPromptProbeTransportComplete
			}
			if probeErr != nil {
				status := preflightStatusFail
				if effectiveTransport == preflightAPIPromptProbeTransportStream && !explicitTransports {
					// Default transport coverage should not block startup when a
					// provider lacks a reliable stream preflight path.
					status = preflightStatusWarn
				}
				details := map[string]any{
					"backend":   "api",
					"model":     target.Model,
					"mode":      target.Mode,
					"transport": effectiveTransport,
				}
				if probe.MaxTokens > 0 {
					details["max_tokens"] = probe.MaxTokens
				}
				if strings.TrimSpace(probe.PolicyHint) != "" {
					details["policy_reason"] = probe.PolicyHint
				}
				report.addCheck(providerPreflightCheck{
					Name:     "provider_prompt_probe",
					Provider: provider,
					Status:   status,
					Message:  fmt.Sprintf("prompt probe failed for model %s (mode=%s transport=%s): %v", target.Model, target.Mode, effectiveTransport, probeErr),
					Details:  details,
				})
				if status == preflightStatusFail {
					return fmt.Errorf("preflight: provider %s api prompt probe failed for model %s (mode=%s transport=%s): %w", provider, target.Model, target.Mode, effectiveTransport, probeErr)
				}
				continue
			}
			details := map[string]any{
				"backend":          "api",
				"model":            target.Model,
				"mode":             target.Mode,
				"transport":        effectiveTransport,
				"response_preview": truncate(strings.TrimSpace(probe.Text), 64),
			}
			if probe.MaxTokens > 0 {
				details["max_tokens"] = probe.MaxTokens
			}
			if strings.TrimSpace(probe.PolicyHint) != "" {
				details["policy_reason"] = probe.PolicyHint
			}
			report.addCheck(providerPreflightCheck{
				Name:     "provider_prompt_probe",
				Provider: provider,
				Status:   preflightStatusPass,
				Message:  fmt.Sprintf("prompt probe succeeded for model %s (mode=%s transport=%s)", target.Model, target.Mode, effectiveTransport),
				Details:  details,
			})
		}
	}
	return nil
}

func runProviderAPIPromptProbe(ctx context.Context, client *llm.Client, provider string, modelID string) (string, error) {
	probe, err := runProviderAPIPromptProbeDetailed(ctx, client, provider, modelID)
	if err != nil {
		return "", err
	}
	return probe.Text, nil
}

func runProviderAPIPromptProbeDetailed(ctx context.Context, client *llm.Client, provider string, modelID string) (preflightAPIPromptProbeResult, error) {
	maxTokens := 16
	target := preflightAPIPromptProbeTarget{
		Provider:  provider,
		Model:     modelID,
		Mode:      "one_shot",
		Transport: preflightAPIPromptProbeTransportComplete,
		Request: llm.Request{
			Provider: provider,
			Model:    modelID,
			Messages: []llm.Message{
				llm.User(preflightPromptProbeText),
			},
			MaxTokens: &maxTokens,
		},
	}
	policy := preflightAPIPromptProbePolicyFromEnv()
	return runProviderAPIPromptProbeTargetWithPolicy(ctx, client, target, policy)
}

func runProviderAPIPromptProbeTarget(ctx context.Context, client *llm.Client, target preflightAPIPromptProbeTarget) (string, error) {
	policy := preflightAPIPromptProbePolicyFromEnv()
	probe, err := runProviderAPIPromptProbeTargetWithPolicy(ctx, client, target, policy)
	if err != nil {
		return "", err
	}
	return probe.Text, nil
}

type preflightAPIPromptProbePolicy struct {
	Timeout   time.Duration
	Retries   int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func configuredAPIPromptProbeTransports(cfg *RunConfigFile) ([]string, bool, error) {
	if cfg != nil && len(cfg.Preflight.PromptProbes.Transports) > 0 {
		out, err := normalizePromptProbeTransports(cfg.Preflight.PromptProbes.Transports)
		if err != nil {
			return nil, false, err
		}
		return out, true, nil
	}
	raw := strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_TRANSPORTS"))
	if raw != "" {
		seen := map[string]bool{}
		parsed := []string{}
		for _, part := range strings.FieldsFunc(raw, func(r rune) bool {
			return r == ',' || r == ';' || r == '|' || r == ' ' || r == '\t' || r == '\n'
		}) {
			v := strings.ToLower(strings.TrimSpace(part))
			switch v {
			case "nonstream", "non-stream":
				v = "complete"
			}
			transport := normalizePromptProbeTransport(v)
			if transport == "" || seen[transport] {
				continue
			}
			seen[transport] = true
			parsed = append(parsed, transport)
		}
		if len(parsed) > 0 {
			return parsed, true, nil
		}
	}
	return []string{preflightAPIPromptProbeTransportComplete, preflightAPIPromptProbeTransportStream}, false, nil
}

func preflightAPIPromptProbePolicyFromEnv() preflightAPIPromptProbePolicy {
	p := preflightAPIPromptProbePolicy{
		Timeout:   defaultPreflightAPIPromptProbeTimeout,
		Retries:   defaultPreflightAPIPromptProbeRetries,
		BaseDelay: defaultPreflightAPIPromptProbeBaseDelay,
		MaxDelay:  defaultPreflightAPIPromptProbeMaxDelay,
	}
	if ms := parseInt(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_TIMEOUT_MS")), 0); ms > 0 {
		p.Timeout = time.Duration(ms) * time.Millisecond
	}
	if retries := parseInt(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_RETRIES")), p.Retries); retries >= 0 {
		p.Retries = retries
	}
	if ms := parseInt(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_BASE_DELAY_MS")), 0); ms > 0 {
		p.BaseDelay = time.Duration(ms) * time.Millisecond
	}
	if ms := parseInt(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_API_PROMPT_PROBE_MAX_DELAY_MS")), 0); ms > 0 {
		p.MaxDelay = time.Duration(ms) * time.Millisecond
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

func preflightAPIPromptProbePolicyFromConfig(cfg *RunConfigFile) preflightAPIPromptProbePolicy {
	p := preflightAPIPromptProbePolicyFromEnv()
	if cfg == nil {
		return p
	}
	if v := cfg.Preflight.PromptProbes.TimeoutMS; v != nil && *v > 0 {
		p.Timeout = time.Duration(*v) * time.Millisecond
	}
	if v := cfg.Preflight.PromptProbes.Retries; v != nil && *v >= 0 {
		p.Retries = *v
	}
	if v := cfg.Preflight.PromptProbes.BaseDelayMS; v != nil && *v > 0 {
		p.BaseDelay = time.Duration(*v) * time.Millisecond
	}
	if v := cfg.Preflight.PromptProbes.MaxDelayMS; v != nil && *v > 0 {
		p.MaxDelay = time.Duration(*v) * time.Millisecond
	}
	if p.MaxDelay < p.BaseDelay {
		p.MaxDelay = p.BaseDelay
	}
	return p
}

func runProviderAPIPromptProbeTargetWithPolicy(ctx context.Context, client *llm.Client, target preflightAPIPromptProbeTarget, policy preflightAPIPromptProbePolicy) (preflightAPIPromptProbeResult, error) {
	result := preflightAPIPromptProbeResult{
		Transport: strings.TrimSpace(target.Transport),
	}
	if result.Transport == "" {
		result.Transport = preflightAPIPromptProbeTransportComplete
	}
	if client == nil {
		return result, fmt.Errorf("api client is nil")
	}
	if strings.TrimSpace(target.Provider) == "" {
		return result, fmt.Errorf("probe target provider is empty")
	}
	if strings.TrimSpace(target.Model) == "" {
		return result, fmt.Errorf("probe target model is empty")
	}
	if policy.Timeout <= 0 {
		policy.Timeout = defaultPreflightAPIPromptProbeTimeout
	}
	if policy.Retries < 0 {
		policy.Retries = 0
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = defaultPreflightAPIPromptProbeBaseDelay
	}
	if policy.MaxDelay <= 0 {
		policy.MaxDelay = defaultPreflightAPIPromptProbeMaxDelay
	}
	if policy.MaxDelay < policy.BaseDelay {
		policy.MaxDelay = policy.BaseDelay
	}

	maxTokens := 16
	if target.Request.MaxTokens == nil {
		target.Request.MaxTokens = &maxTokens
	}
	if strings.TrimSpace(target.Request.Provider) == "" {
		target.Request.Provider = target.Provider
	}
	if strings.TrimSpace(target.Request.Model) == "" {
		target.Request.Model = target.Model
	}
	execPolicy := llm.ExecutionPolicy(target.Provider)
	target.Request = llm.ApplyExecutionPolicy(target.Request, execPolicy)
	if execPolicy.ForceStream {
		result.Transport = preflightAPIPromptProbeTransportStream
	}
	target.Transport = result.Transport
	if target.Request.MaxTokens != nil {
		result.MaxTokens = *target.Request.MaxTokens
	}
	if strings.TrimSpace(execPolicy.Reason) != "" {
		result.PolicyHint = execPolicy.Reason
	}

	var lastErr error
	attempts := policy.Retries + 1
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		probeCtx, cancel := context.WithTimeout(ctx, policy.Timeout)
		responseText, err := runProviderAPIPromptProbeAttempt(probeCtx, client, target)
		cancel()
		if err == nil {
			result.Text = strings.TrimSpace(responseText)
			return result, nil
		}
		lastErr = err
		if attempt == attempts || !shouldRetryPreflightAPIPromptProbe(err) {
			return result, err
		}
		delay := preflightAPIPromptProbeBackoff(policy, attempt-1)
		if sleepErr := waitForPreflightProbeBackoff(ctx, delay); sleepErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return result, ctxErr
			}
			return result, sleepErr
		}
	}
	if lastErr != nil {
		return result, lastErr
	}
	return result, fmt.Errorf("api prompt probe failed")
}

func runProviderAPIPromptProbeAttempt(ctx context.Context, client *llm.Client, target preflightAPIPromptProbeTarget) (string, error) {
	switch strings.ToLower(strings.TrimSpace(target.Transport)) {
	case "", preflightAPIPromptProbeTransportComplete:
		resp, err := client.Complete(ctx, target.Request)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(resp.Text()), nil
	case preflightAPIPromptProbeTransportStream:
		stream, err := client.Stream(ctx, target.Request)
		if err != nil {
			return "", err
		}
		defer func() { _ = stream.Close() }()

		var text strings.Builder
		for {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case ev, ok := <-stream.Events():
				if !ok {
					return "", fmt.Errorf("stream probe ended without finish event")
				}
				switch ev.Type {
				case llm.StreamEventTextDelta:
					text.WriteString(ev.Delta)
				case llm.StreamEventError:
					if ev.Err != nil {
						return "", ev.Err
					}
					return "", fmt.Errorf("stream probe returned error event")
				case llm.StreamEventFinish:
					if ev.Response != nil {
						if t := strings.TrimSpace(ev.Response.Text()); t != "" {
							return t, nil
						}
					}
					return strings.TrimSpace(text.String()), nil
				}
			}
		}
	default:
		return "", fmt.Errorf("unsupported prompt probe transport %q", target.Transport)
	}
}

func preflightAPIPromptProbeBackoff(policy preflightAPIPromptProbePolicy, retryAttempt int) time.Duration {
	if retryAttempt < 0 {
		retryAttempt = 0
	}
	delay := policy.BaseDelay
	for i := 0; i < retryAttempt; i++ {
		if delay >= policy.MaxDelay {
			return policy.MaxDelay
		}
		delay *= 2
	}
	if delay > policy.MaxDelay {
		return policy.MaxDelay
	}
	return delay
}

func waitForPreflightProbeBackoff(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func shouldRetryPreflightAPIPromptProbe(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	var providerErr llm.Error
	if errors.As(err, &providerErr) {
		switch providerErr.StatusCode() {
		case 400, 401, 403, 404, 413, 422:
			return false
		}
		if providerErr.Retryable() {
			return true
		}
		var timeoutErr *llm.RequestTimeoutError
		if errors.As(err, &timeoutErr) {
			return true
		}
		return false
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	for _, hint := range []string{
		"context deadline exceeded",
		"timeout",
		"temporarily unavailable",
		"connection refused",
		"connection reset",
		"rate limit",
		"too many requests",
		"service unavailable",
		"bad gateway",
		"gateway timeout",
		"eof",
	} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func runProviderCLIPreflightChecks(ctx context.Context, g *model.Graph, runtimes map[string]ProviderRuntime, cfg *RunConfigFile, opts RunOptions, report *providerPreflightReport, strictModelProbes bool) error {
	providers := usedCLIProviders(g, runtimes)
	if len(providers) == 0 {
		report.addCheck(providerPreflightCheck{
			Name:    "provider_cli_presence",
			Status:  preflightStatusPass,
			Message: "no cli providers used by graph",
		})
		return nil
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
			return fmt.Errorf("preflight: provider %s executable policy rejected run: %w", provider, err)
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
			return fmt.Errorf("preflight: provider %s cli binary not found: %s", provider, exe)
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
		} else {
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
					return fmt.Errorf("preflight: provider %s capability probe failed: %w", provider, probeErr)
				}
			} else if !probeOutputLooksLikeHelp(provider, output) {
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
					return fmt.Errorf("preflight: provider %s capability probe output not parseable as help", provider)
				}
			} else {
				missing := missingCapabilityTokens(provider, output)
				if len(missing) > 0 {
					report.addCheck(providerPreflightCheck{
						Name:     "provider_cli_capabilities",
						Provider: provider,
						Status:   preflightStatusFail,
						Message:  fmt.Sprintf("required capabilities missing: %s", strings.Join(missing, ", ")),
					})
					return fmt.Errorf("preflight: provider %s capability probe missing required tokens: %s", provider, strings.Join(missing, ", "))
				}
				report.addCheck(providerPreflightCheck{
					Name:     "provider_cli_capabilities",
					Provider: provider,
					Status:   preflightStatusPass,
					Message:  "required capabilities detected",
				})
			}
		}

		models := usedCLIModelsForProvider(g, runtimes, provider, opts)
		if normalizeProviderKey(provider) == "google" && len(models) > 0 {
			if modelProbeMode() == "off" {
				report.addCheck(providerPreflightCheck{
					Name:     "provider_cli_model_access",
					Provider: provider,
					Status:   preflightStatusPass,
					Message:  "model access probe disabled by KILROY_PREFLIGHT_MODEL_PROBES=off",
				})
			} else {
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
					if isGoogleModelNotFound(combined) {
						report.addCheck(providerPreflightCheck{
							Name:     "provider_cli_model_access",
							Provider: provider,
							Status:   preflightStatusFail,
							Message:  fmt.Sprintf("model %s not available to configured account/endpoint", modelID),
						})
						return fmt.Errorf("preflight: provider %s model probe failed for model %s: model not available", provider, modelID)
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
						return fmt.Errorf("preflight: provider %s model probe failed for model %s: %w", provider, modelID, probeErr)
					}
				}
			}
		}
		if err := runProviderCLIPromptProbePreflight(ctx, provider, models, cfg, opts, report); err != nil {
			return err
		}
	}
	return nil
}

func runProviderCLIPromptProbePreflight(ctx context.Context, provider string, models []string, cfg *RunConfigFile, opts RunOptions, report *providerPreflightReport) error {
	if report.PromptProbeMode == "off" {
		report.addCheck(providerPreflightCheck{
			Name:     "provider_prompt_probe",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  "prompt probe disabled by KILROY_PREFLIGHT_PROMPT_PROBES=off (or llm.cli_profile=test_shim default)",
			Details: map[string]any{
				"backend": "cli",
			},
		})
		return nil
	}
	if len(models) == 0 {
		report.addCheck(providerPreflightCheck{
			Name:     "provider_prompt_probe",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  "no models used by graph for cli provider",
			Details: map[string]any{
				"backend": "cli",
			},
		})
		return nil
	}

	// Codex CLI supports two auth modes: API key (OPENAI_API_KEY) and browser-based
	// "chatgpt" auth (session tokens stored in ~/.codex/). The prompt probe runs in
	// an isolated environment where browser auth tokens are unavailable. When no API
	// key is set, skip the probe with a warning rather than failing the entire run.
	// This skip does not apply in test_shim mode where the operator provides a fake executable.
	if usesCodexCLISemantics(provider, "") &&
		strings.TrimSpace(os.Getenv("OPENAI_API_KEY")) == "" &&
		normalizedCLIProfile(cfg) != "test_shim" {
		report.addCheck(providerPreflightCheck{
			Name:     "provider_prompt_probe",
			Provider: provider,
			Status:   preflightStatusWarn,
			Message:  "prompt probe skipped: codex cli detected without OPENAI_API_KEY (likely using chatgpt browser auth which cannot be tested in isolated probe)",
			Details: map[string]any{
				"backend":     "cli",
				"skip_reason": "codex_chatgpt_auth",
			},
		})
		return nil
	}

	for _, modelID := range models {
		if _, err := runProviderCLIPromptProbe(ctx, provider, modelID, cfg, opts); err != nil {
			report.addCheck(providerPreflightCheck{
				Name:     "provider_prompt_probe",
				Provider: provider,
				Status:   preflightStatusFail,
				Message:  fmt.Sprintf("prompt probe failed for model %s: %v", modelID, err),
				Details: map[string]any{
					"backend": "cli",
					"model":   modelID,
				},
			})
			return fmt.Errorf("preflight: provider %s prompt probe failed for model %s: %w", provider, modelID, err)
		}
		report.addCheck(providerPreflightCheck{
			Name:     "provider_prompt_probe",
			Provider: provider,
			Status:   preflightStatusPass,
			Message:  fmt.Sprintf("prompt probe succeeded for model %s", modelID),
			Details: map[string]any{
				"backend": "cli",
				"model":   modelID,
			},
		})
	}
	return nil
}

type preflightCLIPromptProbePolicy struct {
	Timeout   time.Duration
	Retries   int
	BaseDelay time.Duration
	MaxDelay  time.Duration
}

func preflightCLIPromptProbePolicyFromConfig(cfg *RunConfigFile) preflightCLIPromptProbePolicy {
	p := preflightCLIPromptProbePolicy{
		Timeout:   defaultPreflightAPIPromptProbeTimeout,
		Retries:   0,
		BaseDelay: defaultPreflightAPIPromptProbeBaseDelay,
		MaxDelay:  defaultPreflightAPIPromptProbeMaxDelay,
	}
	if cfg == nil {
		return p
	}
	if v := cfg.Preflight.PromptProbes.TimeoutMS; v != nil && *v > 0 {
		p.Timeout = time.Duration(*v) * time.Millisecond
	}
	if v := cfg.Preflight.PromptProbes.Retries; v != nil && *v >= 0 {
		p.Retries = *v
	}
	if v := cfg.Preflight.PromptProbes.BaseDelayMS; v != nil && *v > 0 {
		p.BaseDelay = time.Duration(*v) * time.Millisecond
	}
	if v := cfg.Preflight.PromptProbes.MaxDelayMS; v != nil && *v > 0 {
		p.MaxDelay = time.Duration(*v) * time.Millisecond
	}
	return p
}

func preflightCLIPromptProbeBackoff(policy preflightCLIPromptProbePolicy, retryAttempt int) time.Duration {
	if retryAttempt < 0 {
		retryAttempt = 0
	}
	base := policy.BaseDelay
	if base <= 0 {
		base = defaultPreflightAPIPromptProbeBaseDelay
	}
	maxDelay := policy.MaxDelay
	if maxDelay <= 0 {
		maxDelay = defaultPreflightAPIPromptProbeMaxDelay
	}
	delay := base << retryAttempt
	if delay < base {
		delay = maxDelay
	}
	if delay > maxDelay {
		delay = maxDelay
	}
	return delay
}

func runProviderCLIPromptProbe(ctx context.Context, provider string, modelID string, cfg *RunConfigFile, opts RunOptions) (string, error) {
	if strings.TrimSpace(modelID) == "" {
		return "", fmt.Errorf("model id is empty")
	}
	policy := preflightCLIPromptProbePolicyFromConfig(cfg)
	if policy.Timeout <= 0 {
		policy.Timeout = defaultPreflightAPIPromptProbeTimeout
	}
	attempts := policy.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastOutput string
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		output, failureClass, err := runProviderCLIPromptProbeAttempt(ctx, provider, modelID, cfg, opts, policy)
		if err == nil {
			return output, nil
		}
		lastOutput = output
		lastErr = err
		if attempt == attempts || !strings.EqualFold(strings.TrimSpace(failureClass), failureClassTransientInfra) {
			break
		}
		delay := preflightCLIPromptProbeBackoff(policy, attempt-1)
		if sleepErr := waitForPreflightProbeBackoff(ctx, delay); sleepErr != nil {
			return lastOutput, sleepErr
		}
	}
	return lastOutput, lastErr
}

func runProviderCLIPromptProbeAttempt(ctx context.Context, provider string, modelID string, cfg *RunConfigFile, opts RunOptions, policy preflightCLIPromptProbePolicy) (string, string, error) {
	worktreeForInvocation := firstExistingDir(strings.TrimSpace(opts.WorktreeDir))
	if worktreeForInvocation == "" && cfg != nil {
		worktreeForInvocation = firstExistingDir(strings.TrimSpace(cfg.Repo.Path))
	}
	if worktreeForInvocation == "" {
		worktreeForInvocation = firstExistingDir(strings.TrimSpace(opts.RepoPath))
	}
	if worktreeForInvocation == "" {
		worktreeForInvocation = "."
	}

	probeCtx := ctx
	cancel := func() {}
	if policy.Timeout > 0 {
		probeCtx, cancel = context.WithTimeout(ctx, policy.Timeout)
	}
	defer cancel()

	probeLogsRoot := filepath.Join(opts.LogsRoot, "preflight", "prompt-probe", safePathToken(provider), safePathToken(modelID))
	router := NewCodergenRouterWithRuntimes(cfg, nil, nil)
	execCtx := &Execution{
		LogsRoot:    probeLogsRoot,
		WorktreeDir: worktreeForInvocation,
		Engine: &Engine{
			Options: RunOptions{
				AllowTestShim: opts.AllowTestShim,
			},
		},
	}
	probeNode := model.NewNode("cli")
	probeNode.Attrs["shape"] = "box"

	out, outcome, runErr := router.runCLI(probeCtx, execCtx, probeNode, provider, modelID, preflightPromptProbeText)
	if runErr != nil {
		return out, failureClassDeterministic, fmt.Errorf("probe command failed: %w", runErr)
	}
	if outcome != nil && outcome.Status == runtime.StatusFail {
		failureClass := strings.TrimSpace(fmt.Sprint(outcome.Meta["failure_class"]))
		if failureClass == "" {
			failureClass = failureClassDeterministic
		}
		if errors.Is(probeCtx.Err(), context.DeadlineExceeded) {
			failureClass = failureClassTransientInfra
		}
		if errors.Is(probeCtx.Err(), context.Canceled) {
			failureClass = failureClassTransientInfra
		}
		reason := strings.TrimSpace(outcome.FailureReason)
		if reason == "" {
			reason = "provider cli invocation failed"
		}
		return out, failureClass, fmt.Errorf("probe command failed: %s", reason)
	}
	return out, "", nil
}

func firstExistingDir(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err != nil {
			continue
		}
		if info.IsDir() {
			return candidate
		}
	}
	return ""
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

func promptProbeMode(cfg *RunConfigFile) string {
	if cfg != nil && cfg.Preflight.PromptProbes.Enabled != nil {
		if *cfg.Preflight.PromptProbes.Enabled {
			return "on"
		}
		return "off"
	}
	raw := strings.ToLower(strings.TrimSpace(os.Getenv("KILROY_PREFLIGHT_PROMPT_PROBES")))
	switch raw {
	case "off", "false", "0", "no", "n":
		return "off"
	case "on", "true", "1", "yes", "y":
		return "on"
	}
	if normalizedCLIProfile(cfg) == "test_shim" {
		return "off"
	}
	return "on"
}

func usedCLIProviders(g *model.Graph, runtimes map[string]ProviderRuntime) []string {
	return usedProvidersForBackend(g, runtimes, BackendCLI)
}

func usedAPIProviders(g *model.Graph, runtimes map[string]ProviderRuntime) []string {
	roots := usedProvidersForBackend(g, runtimes, BackendAPI)
	if len(roots) == 0 {
		return nil
	}
	seen := map[string]bool{}
	queue := make([]string, 0, len(roots))
	for _, provider := range roots {
		provider = normalizeProviderKey(provider)
		if provider == "" || seen[provider] {
			continue
		}
		seen[provider] = true
		queue = append(queue, provider)
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rt, ok := runtimes[cur]
		if !ok || rt.Backend != BackendAPI {
			continue
		}
		for _, rawNext := range rt.Failover {
			next := normalizeProviderKey(rawNext)
			if next == "" || seen[next] {
				continue
			}
			nextRT, ok := runtimes[next]
			if !ok || nextRT.Backend != BackendAPI {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}
	providers := make([]string, 0, len(seen))
	for provider := range seen {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

func usedProvidersForBackend(g *model.Graph, runtimes map[string]ProviderRuntime, backend BackendKind) []string {
	used := map[string]bool{}
	if g == nil {
		return nil
	}
	reg := NewDefaultRegistry()
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		provider := normalizeProviderKey(n.Attr("llm_provider", ""))
		if provider == "" {
			continue
		}
		rt, ok := runtimes[provider]
		if !ok || rt.Backend != backend {
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
	return usedModelsForProviderBackend(g, runtimes, provider, BackendCLI, opts)
}

func usedAPIModelsForProvider(g *model.Graph, runtimes map[string]ProviderRuntime, provider string, opts RunOptions) []string {
	return usedModelsForProviderBackend(g, runtimes, provider, BackendAPI, opts)
}

func usedAPIPromptProbeTargetsForProvider(g *model.Graph, runtimes map[string]ProviderRuntime, provider string, opts RunOptions, transports []string, catalog *modeldb.Catalog) ([]preflightAPIPromptProbeTarget, error) {
	provider = normalizeProviderKey(provider)
	if provider == "" || g == nil {
		return nil, nil
	}
	if len(transports) == 0 {
		transports = []string{preflightAPIPromptProbeTransportComplete}
	}

	reg := NewDefaultRegistry()
	seen := map[string]bool{}
	targets := []preflightAPIPromptProbeTarget{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		nodeProvider := normalizeProviderKey(n.Attr("llm_provider", ""))
		if nodeProvider == "" {
			continue
		}
		rt, ok := runtimes[nodeProvider]
		if !ok || rt.Backend != BackendAPI {
			continue
		}
		if !providerInFailoverPath(nodeProvider, provider, runtimes) {
			continue
		}

		sourceModel := modelIDForNode(n)
		if forcedSourceModel, forced := forceModelForProvider(opts.ForceModels, nodeProvider); forced {
			sourceModel = forcedSourceModel
		}
		if sourceModel == "" {
			continue
		}
		modelID := sourceModel
		if provider != nodeProvider {
			if forcedTargetModel, forced := forceModelForProvider(opts.ForceModels, provider); forced {
				modelID = forcedTargetModel
			} else {
				modelID = strings.TrimSpace(pickFailoverModelFromRuntime(provider, runtimes, catalog, sourceModel))
			}
		}
		if modelID == "" {
			continue
		}

		mode := strings.ToLower(strings.TrimSpace(n.Attr("codergen_mode", "")))
		if mode == "" {
			mode = "agent_loop"
		}
		if mode != "one_shot" && mode != "agent_loop" {
			return nil, fmt.Errorf("invalid codergen_mode: %q (want one_shot|agent_loop)", mode)
		}
		reasoning := strings.TrimSpace(n.Attr("reasoning_effort", ""))
		req, err := preflightAPIPromptProbeRequest(provider, modelID, mode, reasoning, runtimes)
		if err != nil {
			return nil, err
		}
		for _, transport := range transports {
			key := strings.Join([]string{
				modelID,
				mode,
				transport,
				reasoning,
			}, "|")
			if seen[key] {
				continue
			}
			seen[key] = true
			targets = append(targets, preflightAPIPromptProbeTarget{
				Provider:  provider,
				Model:     modelID,
				Mode:      mode,
				Transport: transport,
				Request:   req,
			})
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		a, b := targets[i], targets[j]
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Mode != b.Mode {
			return a.Mode < b.Mode
		}
		return a.Transport < b.Transport
	})
	return targets, nil
}

func providerInFailoverPath(sourceProvider string, targetProvider string, runtimes map[string]ProviderRuntime) bool {
	sourceProvider = normalizeProviderKey(sourceProvider)
	targetProvider = normalizeProviderKey(targetProvider)
	if sourceProvider == "" || targetProvider == "" {
		return false
	}
	if sourceProvider == targetProvider {
		return true
	}
	seen := map[string]bool{sourceProvider: true}
	queue := []string{sourceProvider}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		rt, ok := runtimes[cur]
		if !ok || rt.Backend != BackendAPI {
			continue
		}
		for _, rawNext := range rt.Failover {
			next := normalizeProviderKey(rawNext)
			if next == "" {
				continue
			}
			nextRT, ok := runtimes[next]
			if !ok || nextRT.Backend != BackendAPI {
				continue
			}
			if next == targetProvider {
				return true
			}
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, next)
		}
	}
	return false
}

func preflightAPIPromptProbeRequest(provider string, modelID string, mode string, reasoning string, runtimes map[string]ProviderRuntime) (llm.Request, error) {
	maxTokens := 16
	req := llm.Request{
		Provider:  provider,
		Model:     modelID,
		MaxTokens: &maxTokens,
	}
	if strings.TrimSpace(reasoning) != "" {
		v := strings.TrimSpace(reasoning)
		req.ReasoningEffort = &v
	}
	switch mode {
	case "one_shot":
		req.Messages = []llm.Message{llm.User(preflightPromptProbeText)}
		return req, nil
	case "agent_loop":
		toolDefs, err := preflightAPIPromptProbeToolDefinitions(provider, modelID, runtimes)
		if err != nil {
			return llm.Request{}, err
		}
		req.Messages = []llm.Message{
			llm.System(preflightPromptProbeAgentLoopSystem),
			llm.User(preflightPromptProbeAgentLoopText),
		}
		req.Tools = toolDefs
		return req, nil
	default:
		return llm.Request{}, fmt.Errorf("invalid codergen_mode: %q (want one_shot|agent_loop)", mode)
	}
}

func preflightAPIPromptProbeToolDefinitions(provider string, modelID string, runtimes map[string]ProviderRuntime) ([]llm.ToolDefinition, error) {
	provider = normalizeProviderKey(provider)
	if provider == "" {
		return nil, fmt.Errorf("provider is empty")
	}
	rt, ok := runtimes[provider]
	if !ok {
		return nil, fmt.Errorf("provider %s runtime definition missing", provider)
	}
	profile, err := profileForRuntimeProvider(rt, modelID)
	if err != nil {
		return nil, err
	}
	return profile.ToolDefinitions(), nil
}

func usedModelsForProviderBackend(g *model.Graph, runtimes map[string]ProviderRuntime, provider string, backend BackendKind, opts RunOptions) []string {
	provider = normalizeProviderKey(provider)
	if provider == "" || g == nil {
		return nil
	}
	if forcedModel, forced := forceModelForProvider(opts.ForceModels, provider); forced {
		return []string{forcedModel}
	}
	reg := NewDefaultRegistry()
	seen := map[string]bool{}
	models := []string{}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if pr, ok := reg.Resolve(n).(ProviderRequiringHandler); !ok || !pr.RequiresProvider() {
			continue
		}
		nodeProvider := normalizeProviderKey(n.Attr("llm_provider", ""))
		if nodeProvider == "" || nodeProvider != provider {
			continue
		}
		rt, ok := runtimes[nodeProvider]
		if !ok || rt.Backend != backend {
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
	args = insertPromptArg(args, preflightPromptProbeText)
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

type providerProbeOptions struct {
	Stdin string
	Env   []string
	Dir   string
}

func runProviderProbe(ctx context.Context, exePath string, argv []string, timeout time.Duration) (string, error) {
	return runProviderProbeWithOptions(ctx, exePath, argv, timeout, providerProbeOptions{})
}

func runProviderProbeWithOptions(ctx context.Context, exePath string, argv []string, timeout time.Duration, opts providerProbeOptions) (string, error) {
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, exePath, argv...)
	cmd.Stdin = strings.NewReader(opts.Stdin)
	if len(opts.Env) > 0 {
		cmd.Env = opts.Env
	} else {
		cmd.Env = scrubPreflightProbeEnv(os.Environ())
	}
	if strings.TrimSpace(opts.Dir) != "" {
		cmd.Dir = opts.Dir
	}
	setProcessGroupAttr(cmd)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("probe command failed: %w", err)
	}

	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	cleanup := func() {
		_ = terminateProcessGroup(cmd)
		select {
		case <-waitCh:
			return
		case <-time.After(250 * time.Millisecond):
		}
		_ = forceKillProcessGroup(cmd)
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

func safePathToken(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + ('a' - 'A'))
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	s := strings.Trim(b.String(), "._-")
	if s == "" {
		return "unknown"
	}
	if len(s) > 80 {
		return s[:80]
	}
	return s
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
			key == "KILROY_CALL_COUNT_FILE" ||
			key == "CLAUDECODE" {
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
