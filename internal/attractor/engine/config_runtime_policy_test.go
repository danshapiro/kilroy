package engine

import "testing"

func TestRuntimePolicy_DefaultsAndValidation(t *testing.T) {
	cfg := &RunConfigFile{}
	applyConfigDefaults(cfg)

	if cfg.RuntimePolicy.StallTimeoutMS == nil || *cfg.RuntimePolicy.StallTimeoutMS != 600000 {
		t.Fatalf("expected default stall_timeout_ms=600000")
	}
	if cfg.RuntimePolicy.StallCheckIntervalMS == nil || *cfg.RuntimePolicy.StallCheckIntervalMS != 5000 {
		t.Fatalf("expected default stall_check_interval_ms=5000")
	}
	if cfg.RuntimePolicy.MaxLLMRetries == nil || *cfg.RuntimePolicy.MaxLLMRetries != 6 {
		t.Fatalf("expected default max_llm_retries=6")
	}

	cfg.Version = 1
	cfg.Repo.Path = "/tmp/repo"
	cfg.CXDB.BinaryAddr = "127.0.0.1:1"
	cfg.CXDB.HTTPBaseURL = "http://127.0.0.1:1"
	cfg.ModelDB.OpenRouterModelInfoPath = "/tmp/catalog.json"

	zero := 0
	cfg.RuntimePolicy.MaxLLMRetries = &zero
	if err := validateConfig(cfg); err != nil {
		t.Fatalf("max_llm_retries=0 should be valid: %v", err)
	}

	neg := -1
	cfg.RuntimePolicy.MaxLLMRetries = &neg
	if err := validateConfig(cfg); err == nil {
		t.Fatal("expected validation error for negative max_llm_retries")
	}
}
