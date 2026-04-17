package engine

import (
	"testing"
)

func TestCodexStateDBMaxRetries_Default(t *testing.T) {
	t.Setenv("KILROY_CODEX_STATE_DB_MAX_RETRIES", "")
	if got := codexStateDBMaxRetries(); got != 2 {
		t.Fatalf("default: got %d want 2", got)
	}
}

func TestCodexStateDBMaxRetries_Configured(t *testing.T) {
	t.Setenv("KILROY_CODEX_STATE_DB_MAX_RETRIES", "3")
	if got := codexStateDBMaxRetries(); got != 3 {
		t.Fatalf("configured: got %d want 3", got)
	}
}

func TestCodexStateDBMaxRetries_Zero(t *testing.T) {
	t.Setenv("KILROY_CODEX_STATE_DB_MAX_RETRIES", "0")
	if got := codexStateDBMaxRetries(); got != 0 {
		t.Fatalf("zero: got %d want 0", got)
	}
}

func TestCodexStateDBMaxRetries_InvalidFallsBackToDefault(t *testing.T) {
	t.Setenv("KILROY_CODEX_STATE_DB_MAX_RETRIES", "not-a-number")
	if got := codexStateDBMaxRetries(); got != 2 {
		t.Fatalf("invalid: got %d want 2", got)
	}
}

func TestIsStateDBDiscrepancy(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   bool
	}{
		{"missing rollout", "Error: state db missing rollout path for ...", true},
		{"record discrepancy", "state db record_discrepancy detected", true},
		{"no match", "some other error", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isStateDBDiscrepancy(tc.stderr); got != tc.want {
				t.Fatalf("isStateDBDiscrepancy(%q): got %v want %v", tc.stderr, got, tc.want)
			}
		})
	}
}
