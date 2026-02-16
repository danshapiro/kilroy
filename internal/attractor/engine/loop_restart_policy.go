package engine

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

const (
	failureClassTransientInfra       = "transient_infra"
	failureClassDeterministic        = "deterministic"
	failureClassCanceled             = "canceled"
	failureClassBudgetExhausted      = "budget_exhausted"
	failureClassCompilationLoop      = "compilation_loop"
	failureClassStructural           = "structural"
	defaultLoopRestartSignatureLimit = 3
	// 0 disables visit-count cycle breaking unless max_node_visits is explicitly set.
	defaultMaxNodeVisits = 0
)

var (
	failureSignatureWhitespaceRE = regexp.MustCompile(`\s+`)
	failureSignatureHexRE        = regexp.MustCompile(`\b[0-9a-f]{7,64}\b`)
	failureSignatureDigitsRE     = regexp.MustCompile(`\b\d+\b`)
	transientInfraReasonHints    = []string{
		"timeout",
		"timed out",
		"context deadline exceeded",
		"connection refused",
		"connection reset",
		"broken pipe",
		"tls handshake timeout",
		"i/o timeout",
		"no route to host",
		"temporary failure",
		"temporarily unavailable",
		"try again",
		"rate limit",
		"too many requests",
		"service unavailable",
		"gateway timeout",
		"econnrefused",
		"econnreset",
		"dial tcp",
		"transport is closing",
		"stream disconnected",
		"stream closed before",
		"502",
		"503",
		"504",
	}
	budgetExhaustedReasonHints = []string{
		"turn limit",
		"max_turns",
		"max turns",
		"token limit reached",
		"token limit exceeded",
		"max tokens",
		"max_tokens",
		"context length exceeded",
		"context window exceeded",
		"budget exhausted",
	}
	structuralReasonHints = []string{
		"write_scope_violation",
		"write scope violation",
		"scope violation",
	}
)

func isFailureLoopRestartOutcome(out runtime.Outcome) bool {
	return out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry
}

func classifyFailureClass(out runtime.Outcome) string {
	if !isFailureLoopRestartOutcome(out) {
		return ""
	}
	if hinted := normalizedFailureClass(readFailureClassHint(out)); hinted != "" {
		return hinted
	}

	reason := strings.ToLower(strings.TrimSpace(out.FailureReason))
	if reason == "" {
		return failureClassDeterministic
	}
	if strings.Contains(reason, "canceled") || strings.Contains(reason, "cancelled") {
		return failureClassCanceled
	}
	for _, hint := range transientInfraReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassTransientInfra
		}
	}
	for _, hint := range budgetExhaustedReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassBudgetExhausted
		}
	}
	for _, hint := range structuralReasonHints {
		if strings.Contains(reason, hint) {
			return failureClassStructural
		}
	}
	return failureClassDeterministic
}

func readFailureClassHint(out runtime.Outcome) string {
	if out.Meta != nil {
		if raw, ok := out.Meta["failure_class"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	if out.ContextUpdates != nil {
		if raw, ok := out.ContextUpdates["failure_class"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func readFailureSignatureHint(out runtime.Outcome) string {
	if out.Meta != nil {
		if raw, ok := out.Meta["failure_signature"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	if out.ContextUpdates != nil {
		if raw, ok := out.ContextUpdates["failure_signature"]; ok {
			if s := strings.TrimSpace(fmt.Sprint(raw)); s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func normalizedFailureClass(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "<nil>":
		return ""
	case "transient", "transient_infra", "transient-infra", "infra_transient", "transient infra", "infrastructure_transient", "retryable":
		return failureClassTransientInfra
	case "canceled", "cancelled":
		return failureClassCanceled
	case "deterministic", "non_transient", "non-transient", "permanent", "logic", "product":
		return failureClassDeterministic
	case "budget_exhausted", "budget-exhausted", "budget exhausted", "budget":
		return failureClassBudgetExhausted
	case "compilation_loop", "compilation-loop", "compilation loop", "compile_loop", "compile-loop":
		return failureClassCompilationLoop
	case "structural", "structure", "scope_violation", "write_scope_violation":
		return failureClassStructural
	default:
		return failureClassDeterministic
	}
}

func normalizedFailureClassOrDefault(raw string) string {
	if cls := normalizedFailureClass(raw); cls != "" {
		return cls
	}
	return failureClassDeterministic
}

// isSignatureTrackedFailureClass returns true if the failure class should be
// tracked by the deterministic failure cycle breaker. Structural failures are
// included so they accumulate signatures in the main loop (in subgraphs they
// are caught earlier by the immediate structural abort).
func isSignatureTrackedFailureClass(failureClass string) bool {
	cls := normalizedFailureClassOrDefault(failureClass)
	return cls == failureClassDeterministic || cls == failureClassStructural
}

func loopRestartSignatureLimit(g *model.Graph) int {
	if g == nil {
		return defaultLoopRestartSignatureLimit
	}
	limit := parseInt(g.Attrs["loop_restart_signature_limit"], defaultLoopRestartSignatureLimit)
	if limit < 1 {
		return defaultLoopRestartSignatureLimit
	}
	return limit
}

func maxNodeVisits(g *model.Graph) int {
	if g == nil {
		return defaultMaxNodeVisits
	}
	limit := parseInt(g.Attrs["max_node_visits"], defaultMaxNodeVisits)
	if limit < 1 {
		return defaultMaxNodeVisits
	}
	return limit
}

func restartFailureSignature(nodeID string, out runtime.Outcome, failureClass string) string {
	if !isFailureLoopRestartOutcome(out) {
		return ""
	}
	reason := normalizeFailureReason(readFailureSignatureHint(out))
	if reason == "" {
		reason = normalizeFailureReason(out.FailureReason)
	}
	if reason == "" {
		reason = "status=" + strings.ToLower(strings.TrimSpace(string(out.Status)))
	}
	return strings.TrimSpace(nodeID) + "|" + normalizedFailureClassOrDefault(failureClass) + "|" + reason
}

// loopRestartPersistKeyNames returns the list of context keys configured to persist
// across loop_restart iterations via the loop_restart_persist_keys graph attribute.
func loopRestartPersistKeyNames(g *model.Graph) []string {
	if g == nil {
		return nil
	}
	raw := strings.TrimSpace(g.Attrs["loop_restart_persist_keys"])
	if raw == "" {
		return nil
	}
	var keys []string
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if key != "" {
			keys = append(keys, key)
		}
	}
	return keys
}

func normalizeFailureReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return ""
	}
	reason = failureSignatureHexRE.ReplaceAllString(reason, "<hex>")
	reason = failureSignatureDigitsRE.ReplaceAllString(reason, "<n>")
	reason = failureSignatureWhitespaceRE.ReplaceAllString(reason, " ")
	reason = strings.TrimSpace(reason)
	if len(reason) > 240 {
		reason = reason[:240]
	}
	return reason
}
