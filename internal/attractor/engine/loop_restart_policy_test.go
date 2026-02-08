package engine

import (
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestClassifyFailureClass_PreservesExplicitTransientInfra(t *testing.T) {
	out := runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "some failure",
		Meta: map[string]any{
			failureMetaClass: string(failureClassTransientInfra),
		},
	}
	if got := classifyFailureClass(out); got != failureClassTransientInfra {
		t.Fatalf("class=%q want=%q", got, failureClassTransientInfra)
	}
}

func TestClassifyFailureClass_FailClosedToDeterministic(t *testing.T) {
	cases := []runtime.Outcome{
		{Status: runtime.StatusFail, FailureReason: "some unknown error"},
		{
			Status:        runtime.StatusFail,
			FailureReason: "some unknown error",
			Meta: map[string]any{
				failureMetaClass: "not-a-real-class",
			},
		},
		{
			Status:        runtime.StatusFail,
			FailureReason: "some unknown error",
			Meta: map[string]any{
				failureMetaClass: "",
			},
		},
	}
	for i, out := range cases {
		if got := classifyFailureClass(out); got != failureClassDeterministic {
			t.Fatalf("case=%d class=%q want=%q", i, got, failureClassDeterministic)
		}
	}
}

func TestClassifyFailureClass_ProviderContractErrorsAreDeterministic(t *testing.T) {
	cases := []string{
		"unknown flag: --verbose",
		"provider contract mismatch: unsupported argument --stream-json",
		"invalid schema for response_format",
	}
	for _, reason := range cases {
		out := runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: reason,
		}
		if got := classifyFailureClass(out); got != failureClassDeterministic {
			t.Fatalf("reason=%q class=%q want=%q", reason, got, failureClassDeterministic)
		}
	}
}

func TestClassifyFailureClass_NetworkAndTimeoutAreTransient(t *testing.T) {
	cases := []string{
		"request timeout after 30s",
		"connection reset by peer",
		"429 too many requests",
	}
	for _, reason := range cases {
		out := runtime.Outcome{
			Status:        runtime.StatusRetry,
			FailureReason: reason,
		}
		if got := classifyFailureClass(out); got != failureClassTransientInfra {
			t.Fatalf("reason=%q class=%q want=%q", reason, got, failureClassTransientInfra)
		}
	}
}

func TestRestartFailureSignature_StableForEquivalentDeterministicFailures(t *testing.T) {
	outA := runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "Unknown flag: --verbose",
		Meta: map[string]any{
			failureMetaClass: string(failureClassDeterministic),
		},
	}
	outB := runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: "unknown    flag: --verbose   ",
		Meta: map[string]any{
			failureMetaClass: string(failureClassDeterministic),
		},
	}

	sigA := restartFailureSignature(outA)
	sigB := restartFailureSignature(outB)
	if sigA == "" {
		t.Fatalf("signature for outA should be non-empty")
	}
	if sigB == "" {
		t.Fatalf("signature for outB should be non-empty")
	}
	if sigA != sigB {
		t.Fatalf("signatures differ: %q vs %q", sigA, sigB)
	}
}
