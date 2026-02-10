package engine

import "testing"

func TestConfiguredAPIPromptProbeTransports_FromConfig(t *testing.T) {
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)
	enabled := true
	cfg.Preflight.PromptProbes.Enabled = &enabled
	cfg.Preflight.PromptProbes.Transports = []string{"complete", "stream"}

	got, explicit, err := configuredAPIPromptProbeTransports(cfg)
	if err != nil {
		t.Fatalf("configuredAPIPromptProbeTransports: %v", err)
	}
	if len(got) != 2 || got[0] != "complete" || got[1] != "stream" {
		t.Fatalf("unexpected transports: %v", got)
	}
	if !explicit {
		t.Fatal("expected transports to be marked explicit")
	}
}

func TestConfiguredAPIPromptProbeTransports_InvalidConfigValueErrors(t *testing.T) {
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)
	cfg.Preflight.PromptProbes.Transports = []string{"strem"}

	if _, _, err := configuredAPIPromptProbeTransports(cfg); err == nil {
		t.Fatal("expected invalid transport to fail")
	}
}

func TestPromptProbeMode_ConfigOverridesEnv(t *testing.T) {
	t.Setenv("KILROY_PREFLIGHT_PROMPT_PROBES", "off")
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)
	on := true
	cfg.Preflight.PromptProbes.Enabled = &on
	if got := promptProbeMode(cfg); got != "on" {
		t.Fatalf("mode=%q want on", got)
	}
}
