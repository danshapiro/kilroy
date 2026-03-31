package runtime

import (
	"strings"
	"testing"
)

func TestParseStageStatus_CanonicalAndLegacy(t *testing.T) {
	cases := []struct {
		in   string
		want StageStatus
	}{
		{"success", StatusSuccess},
		{"degraded_success", StatusDegradedSuccess},
		{"partial_success", StatusPartialSuccess},
		{"retry", StatusRetry},
		{"fail", StatusFail},
		{"skipped", StatusSkipped},
		// Compatibility aliases.
		{"ok", StatusSuccess},
		{"degraded-success", StatusDegradedSuccess},
		{"degradedsuccess", StatusDegradedSuccess},
		{"error", StatusFail},
		{"SUCCESS", StatusSuccess},
		{"FAIL", StatusFail},
	}
	for _, tc := range cases {
		got, err := ParseStageStatus(tc.in)
		if err != nil {
			t.Fatalf("ParseStageStatus(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseStageStatus(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseStageStatus_CustomOutcomes(t *testing.T) {
	// Custom outcome values used in reference dotfiles (semport.dot, consensus_task.dot).
	cases := []struct {
		in   string
		want StageStatus
	}{
		{"process", StageStatus("process")},
		{"done", StageStatus("done")},
		{"port", StageStatus("port")},
		{"needs_dod", StageStatus("needs_dod")},
		{"has_dod", StageStatus("has_dod")},
		{"yes", StageStatus("yes")},
		{"PROCESS", StageStatus("process")}, // normalized to lowercase
	}
	for _, tc := range cases {
		got, err := ParseStageStatus(tc.in)
		if err != nil {
			t.Fatalf("ParseStageStatus(%q) error: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("ParseStageStatus(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
	// Empty string is still an error.
	if _, err := ParseStageStatus(""); err == nil {
		t.Fatalf("expected error for empty status")
	}
	if _, err := ParseStageStatus("  "); err == nil {
		t.Fatalf("expected error for whitespace-only status")
	}
}

func TestStageStatus_IsCanonical(t *testing.T) {
	if !StatusSuccess.IsCanonical() {
		t.Fatalf("StatusSuccess should be canonical")
	}
	if !StatusDegradedSuccess.IsCanonical() {
		t.Fatalf("StatusDegradedSuccess should be canonical")
	}
	if !StatusFail.IsCanonical() {
		t.Fatalf("StatusFail should be canonical")
	}
	if StageStatus("process").IsCanonical() {
		t.Fatalf("custom status 'process' should not be canonical")
	}
}

func TestOutcome_Validate_FailureReasonRequiredForFailAndRetry(t *testing.T) {
	if err := (Outcome{Status: StatusFail}).Validate(); err == nil {
		t.Fatalf("expected error for missing failure_reason when status=fail")
	}
	if err := (Outcome{Status: StatusRetry}).Validate(); err == nil {
		t.Fatalf("expected error for missing failure_reason when status=retry")
	}
	if err := (Outcome{Status: StatusSuccess}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDecodeOutcomeJSON_AcceptsCanonicalAndLegacyShapes(t *testing.T) {
	// Canonical metaspec shape.
	o1, err := DecodeOutcomeJSON([]byte(`{"status":"success","preferred_label":"x","details":{"k":"v"}}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON canonical: %v", err)
	}
	if o1.Status != StatusSuccess || o1.PreferredLabel != "x" {
		t.Fatalf("canonical decode: %+v", o1)
	}
	if got, ok := o1.Details.(map[string]any); !ok || got["k"] != "v" {
		t.Fatalf("canonical details not preserved: %+v", o1.Details)
	}
	if o1.SuggestedNextIDs == nil || o1.ContextUpdates == nil || o1.Meta == nil {
		t.Fatalf("expected non-nil slices/maps after canonicalize: %+v", o1)
	}

	// Legacy-ish shape.
	o2, err := DecodeOutcomeJSON([]byte(`{"outcome":"SUCCESS","preferred_next_label":"Yes","suggested_next_ids":["a"],"context_updates":{"k":"v"},"notes":"n","details":["d1","d2"]}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON legacy: %v", err)
	}
	if o2.Status != StatusSuccess || o2.PreferredLabel != "Yes" {
		t.Fatalf("legacy decode: %+v", o2)
	}
	if got, ok := o2.Details.([]any); !ok || len(got) != 2 || got[0] != "d1" || got[1] != "d2" {
		t.Fatalf("legacy details not preserved: %+v", o2.Details)
	}
	if len(o2.SuggestedNextIDs) != 1 || o2.SuggestedNextIDs[0] != "a" {
		t.Fatalf("legacy suggested_next_ids: %+v", o2.SuggestedNextIDs)
	}
	if o2.ContextUpdates["k"] != "v" {
		t.Fatalf("legacy context_updates: %+v", o2.ContextUpdates)
	}
}

func TestDecodeOutcomeJSON_CustomOutcome_Canonical(t *testing.T) {
	// Custom outcome via canonical status.json format.
	o, err := DecodeOutcomeJSON([]byte(`{"status":"process","context_updates":{"decision":"process"}}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON custom canonical: %v", err)
	}
	if o.Status != StageStatus("process") {
		t.Fatalf("expected status 'process', got %q", o.Status)
	}
	if o.ContextUpdates["decision"] != "process" {
		t.Fatalf("expected context_updates preserved, got %+v", o.ContextUpdates)
	}
}

func TestDecodeOutcomeJSON_CustomOutcome_Legacy(t *testing.T) {
	// Custom outcome via legacy format.
	o, err := DecodeOutcomeJSON([]byte(`{"outcome":"done","notes":"all features complete"}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON custom legacy: %v", err)
	}
	if o.Status != StageStatus("done") {
		t.Fatalf("expected status 'done', got %q", o.Status)
	}
}

func TestDecodeOutcomeJSON_LegacyFailDetails_PopulatesFailureReason(t *testing.T) {
	o, err := DecodeOutcomeJSON([]byte(`{"outcome":"fail","details":["module download blocked"],"notes":"verify step failed"}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if o.Status != StatusFail {
		t.Fatalf("status: got %q want %q", o.Status, StatusFail)
	}
	if strings.TrimSpace(o.FailureReason) == "" {
		t.Fatalf("expected non-empty failure_reason")
	}
	if !strings.Contains(strings.ToLower(o.FailureReason), "module") {
		t.Fatalf("failure_reason should summarize details, got: %q", o.FailureReason)
	}
}

func TestDecodeOutcomeJSON_DegradedSuccessWithVerification(t *testing.T) {
	input := `{
		"status": "degraded_success",
		"notes": "implementation complete but tsc blocked by DNS failure",
		"verification": {
			"status": "blocked",
			"blocked_reason": "npm registry DNS failure",
			"commands": [
				{"command": "npx tsc", "exit_code": 1, "blocked": true, "reason": "EAI_AGAIN"}
			]
		}
	}`
	o, err := DecodeOutcomeJSON([]byte(input))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if o.Status != StatusDegradedSuccess {
		t.Fatalf("status: got %q want %q", o.Status, StatusDegradedSuccess)
	}
	if o.Verification == nil {
		t.Fatal("expected non-nil verification")
	}
	if o.Verification.Status != "blocked" {
		t.Fatalf("verification status: got %q want %q", o.Verification.Status, "blocked")
	}
	if o.Verification.BlockedReason != "npm registry DNS failure" {
		t.Fatalf("verification blocked_reason: got %q", o.Verification.BlockedReason)
	}
	if len(o.Verification.Commands) != 1 {
		t.Fatalf("expected 1 verification command, got %d", len(o.Verification.Commands))
	}
	cmd := o.Verification.Commands[0]
	if cmd.Command != "npx tsc" || cmd.ExitCode != 1 || !cmd.Blocked {
		t.Fatalf("unexpected verification command: %+v", cmd)
	}
}

func TestDecodeOutcomeJSON_SuccessWithPassedVerification(t *testing.T) {
	input := `{
		"status": "success",
		"verification": {
			"status": "passed",
			"commands": [
				{"command": "go test ./...", "exit_code": 0},
				{"command": "go vet ./...", "exit_code": 0}
			]
		}
	}`
	o, err := DecodeOutcomeJSON([]byte(input))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if o.Status != StatusSuccess {
		t.Fatalf("status: got %q want %q", o.Status, StatusSuccess)
	}
	if o.Verification == nil {
		t.Fatal("expected non-nil verification")
	}
	if o.Verification.Status != "passed" {
		t.Fatalf("verification status: got %q want %q", o.Verification.Status, "passed")
	}
	if len(o.Verification.Commands) != 2 {
		t.Fatalf("expected 2 verification commands, got %d", len(o.Verification.Commands))
	}
}

func TestDecodeOutcomeJSON_LegacyRetryDetails_PopulatesFailureReason(t *testing.T) {
	o, err := DecodeOutcomeJSON([]byte(`{"outcome":"retry","details":"transient timeout"}`))
	if err != nil {
		t.Fatalf("DecodeOutcomeJSON: %v", err)
	}
	if o.Status != StatusRetry {
		t.Fatalf("status: got %q want %q", o.Status, StatusRetry)
	}
	if strings.TrimSpace(o.FailureReason) == "" {
		t.Fatalf("expected non-empty failure_reason")
	}
}
