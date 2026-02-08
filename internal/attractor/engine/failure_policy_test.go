package engine

import (
	"testing"

	"github.com/strongdm/kilroy/internal/attractor/runtime"
)

func TestShouldRetryOutcome_ClassGated(t *testing.T) {
	cases := []struct {
		name  string
		out   runtime.Outcome
		class string
		want  bool
	}{
		{
			name:  "fail transient retries",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "temporary timeout"},
			class: failureClassTransientInfra,
			want:  true,
		},
		{
			name:  "fail deterministic does not retry",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "contract mismatch"},
			class: failureClassDeterministic,
			want:  false,
		},
		{
			name:  "retry transient retries",
			out:   runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "retry please"},
			class: failureClassTransientInfra,
			want:  true,
		},
		{
			name:  "retry deterministic does not retry",
			out:   runtime.Outcome{Status: runtime.StatusRetry, FailureReason: "permanent"},
			class: failureClassDeterministic,
			want:  false,
		},
		{
			name:  "unknown class defaults fail-closed",
			out:   runtime.Outcome{Status: runtime.StatusFail, FailureReason: "unknown"},
			class: "",
			want:  false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetryOutcome(tc.out, tc.class); got != tc.want {
				t.Fatalf("shouldRetryOutcome(%q,%q)=%v want %v", tc.out.Status, tc.class, got, tc.want)
			}
		})
	}
}

func TestShouldRetryOutcome_NonFailureStatusesNeverRetry(t *testing.T) {
	statuses := []runtime.StageStatus{
		runtime.StatusSuccess,
		runtime.StatusPartialSuccess,
		runtime.StatusSkipped,
	}
	for _, st := range statuses {
		if shouldRetryOutcome(runtime.Outcome{Status: st}, failureClassTransientInfra) {
			t.Fatalf("status=%q should never retry", st)
		}
	}
}
