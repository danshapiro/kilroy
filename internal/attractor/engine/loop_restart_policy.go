package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

type failureClass string

const (
	failureClassTransientInfra failureClass = "transient_infra"
	failureClassDeterministic  failureClass = "deterministic"

	failureMetaClass     = "failure_class"
	failureMetaSignature = "failure_signature"
)

func normalizedFailureClass(raw string) failureClass {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "transient", "transient_infra", "transientinfra":
		return failureClassTransientInfra
	case "deterministic":
		return failureClassDeterministic
	default:
		return failureClassDeterministic
	}
}

func classifyFailureClass(out runtime.Outcome) failureClass {
	if raw, ok := metaString(out.Meta, failureMetaClass); ok {
		if c, ok := parseFailureClass(raw); ok {
			return c
		}
	}

	reason := strings.ToLower(strings.TrimSpace(out.FailureReason))
	if reason == "" {
		return failureClassDeterministic
	}

	if hasAny(reason,
		"unknown flag",
		"unsupported flag",
		"unsupported argument",
		"invalid argument",
		"invalid option",
		"unrecognized option",
		"requires an argument",
		"missing required",
		"not a valid branch name",
		"path does not exist",
		"invalid schema",
		"invalid_json_schema",
		"contract mismatch",
		"unsupported capability",
	) {
		return failureClassDeterministic
	}
	if hasAny(reason,
		"timeout",
		"timed out",
		"temporary",
		"connection reset",
		"connection refused",
		"connection aborted",
		"connection closed",
		"too many requests",
		"rate limit",
		" 429 ",
		" 502 ",
		" 503 ",
		" 504 ",
		"econnreset",
		"econnrefused",
		"service unavailable",
		"try again",
	) {
		return failureClassTransientInfra
	}
	return failureClassDeterministic
}

func restartFailureSignature(out runtime.Outcome) string {
	class := classifyFailureClass(out)
	reason := normalizeFailureReason(out.FailureReason)
	if reason == "" {
		reason = "unknown"
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s", class, reason)))
	return hex.EncodeToString(sum[:])[:24]
}

func shouldRetryOutcome(out runtime.Outcome) bool {
	if out.Status != runtime.StatusFail && out.Status != runtime.StatusRetry {
		return false
	}
	return classifyFailureClass(out) == failureClassTransientInfra
}

func parseFailureClass(raw string) (failureClass, bool) {
	norm := strings.ToLower(strings.TrimSpace(raw))
	switch norm {
	case "transient", "transient_infra", "transientinfra":
		return failureClassTransientInfra, true
	case "deterministic":
		return failureClassDeterministic, true
	default:
		return failureClassDeterministic, false
	}
}

func metaString(meta map[string]any, key string) (string, bool) {
	if len(meta) == 0 {
		return "", false
	}
	v, ok := meta[key]
	if !ok || v == nil {
		return "", false
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" || s == "<nil>" {
		return "", false
	}
	return s, true
}

func hasAny(s string, markers ...string) bool {
	s = strings.ToLower(s)
	for _, marker := range markers {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
}

func normalizeFailureReason(reason string) string {
	reason = strings.ToLower(strings.TrimSpace(reason))
	if reason == "" {
		return ""
	}
	return strings.Join(strings.Fields(reason), " ")
}
