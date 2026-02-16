package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
)

type StageStatus string

const (
	StatusSuccess        StageStatus = "success"
	StatusPartialSuccess StageStatus = "partial_success"
	StatusRetry          StageStatus = "retry"
	StatusFail           StageStatus = "fail"
	StatusSkipped        StageStatus = "skipped"
)

func ParseStageStatus(s string) (StageStatus, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "success", "ok":
		return StatusSuccess, nil
	case "partial_success", "partialsuccess", "partial-success":
		return StatusPartialSuccess, nil
	case "retry":
		return StatusRetry, nil
	case "fail", "failure", "error":
		return StatusFail, nil
	case "skipped", "skip":
		return StatusSkipped, nil
	default:
		// Custom outcome values (e.g. "process", "done", "port") are used
		// in reference dotfiles (semport.dot, consensus_task.dot) for
		// multi-way conditional routing. Pass them through as-is.
		normalized := strings.ToLower(strings.TrimSpace(s))
		if normalized == "" {
			return "", fmt.Errorf("invalid stage status: empty string")
		}
		return StageStatus(normalized), nil
	}
}

func (s StageStatus) Valid() bool {
	_, err := ParseStageStatus(string(s))
	return err == nil
}

// IsCanonical returns true if the status is one of the five canonical values
// (success, partial_success, retry, fail, skipped) rather than a custom routing value.
func (s StageStatus) IsCanonical() bool {
	switch s {
	case StatusSuccess, StatusPartialSuccess, StatusRetry, StatusFail, StatusSkipped:
		return true
	default:
		return false
	}
}

type Outcome struct {
	Status           StageStatus    `json:"status"`
	PreferredLabel   string         `json:"preferred_label,omitempty"`
	SuggestedNextIDs []string       `json:"suggested_next_ids,omitempty"`
	ContextUpdates   map[string]any `json:"context_updates,omitempty"`
	Notes            string         `json:"notes,omitempty"`
	FailureReason    string         `json:"failure_reason,omitempty"`
	// Optional: handler-specific metadata (not used for routing).
	Meta map[string]any `json:"meta,omitempty"`
}

func (o Outcome) Canonicalize() (Outcome, error) {
	st, err := ParseStageStatus(string(o.Status))
	if err != nil {
		return Outcome{}, err
	}
	o.Status = st
	if o.ContextUpdates == nil {
		o.ContextUpdates = map[string]any{}
	}
	if o.SuggestedNextIDs == nil {
		o.SuggestedNextIDs = []string{}
	}
	if o.Meta == nil {
		o.Meta = map[string]any{}
	}
	return o, nil
}

func (o Outcome) Validate() error {
	co, err := o.Canonicalize()
	if err != nil {
		return err
	}
	if (co.Status == StatusFail || co.Status == StatusRetry) && strings.TrimSpace(co.FailureReason) == "" {
		return fmt.Errorf("failure_reason must be non-empty when status=%q", co.Status)
	}
	return nil
}

func DecodeOutcomeJSON(b []byte) (Outcome, error) {
	// Metaspec's status.json is canonical. Accept a few common legacy shapes too.
	//
	// Canonical:
	// {"status":"success","preferred_label":"","suggested_next_ids":[],"context_updates":{},"notes":"","failure_reason":""}
	var o Outcome
	if err := json.Unmarshal(b, &o); err == nil && o.Status != "" {
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err == nil {
			if o.Meta == nil {
				o.Meta = map[string]any{}
			}
			if fc := strings.TrimSpace(fmt.Sprint(raw["failure_class"])); fc != "" && fc != "<nil>" {
				o.Meta["failure_class"] = fc
			}
			if sig := strings.TrimSpace(fmt.Sprint(raw["failure_signature"])); sig != "" && sig != "<nil>" {
				o.Meta["failure_signature"] = sig
			}
		}
		return o.Canonicalize()
	}

	// Legacy-ish (attractor-spec Appendix C):
	// {"outcome":"success","preferred_next_label":"...","suggested_next_ids":[...],"context_updates":{...},"notes":"..."}
	var legacy struct {
		Outcome            string         `json:"outcome"`
		PreferredNextLabel string         `json:"preferred_next_label"`
		SuggestedNextIDs   []string       `json:"suggested_next_ids"`
		ContextUpdates     map[string]any `json:"context_updates"`
		Notes              string         `json:"notes"`
		FailureReason      string         `json:"failure_reason"`
		Details            any            `json:"details"`
	}
	if err := json.Unmarshal(b, &legacy); err != nil {
		return Outcome{}, err
	}
	status := StageStatus(legacy.Outcome)
	o = Outcome{
		Status:           status,
		PreferredLabel:   legacy.PreferredNextLabel,
		SuggestedNextIDs: legacy.SuggestedNextIDs,
		ContextUpdates:   legacy.ContextUpdates,
		Notes:            legacy.Notes,
		FailureReason:    legacyFailureReason(status, legacy.FailureReason, legacy.Details, legacy.Notes),
	}
	return o.Canonicalize()
}

func legacyFailureReason(status StageStatus, failureReason string, details any, notes string) string {
	if fr := strings.TrimSpace(failureReason); fr != "" {
		return fr
	}
	st, err := ParseStageStatus(string(status))
	if err != nil || (st != StatusFail && st != StatusRetry) {
		return ""
	}
	if d := summarizeLegacyDetails(details); d != "" {
		return d
	}
	if n := strings.TrimSpace(notes); n != "" {
		return n
	}
	return "legacy fail outcome missing failure_reason"
}

func summarizeLegacyDetails(details any) string {
	switch v := details.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s := summarizeLegacyDetails(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		for _, key := range []string{"failure_reason", "reason", "message", "error", "details"} {
			if s := strings.TrimSpace(fmt.Sprint(v[key])); s != "" && s != "<nil>" {
				return s
			}
		}
		b, err := json.Marshal(v)
		if err != nil {
			return strings.TrimSpace(fmt.Sprint(v))
		}
		return strings.TrimSpace(string(b))
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "<nil>" {
			return ""
		}
		return s
	}
}
