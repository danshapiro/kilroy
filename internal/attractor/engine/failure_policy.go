package engine

import "github.com/strongdm/kilroy/internal/attractor/runtime"

func shouldRetryOutcome(out runtime.Outcome, failureClass string) bool {
	if out.Status != runtime.StatusFail && out.Status != runtime.StatusRetry {
		return false
	}
	return normalizedFailureClassOrDefault(failureClass) == failureClassTransientInfra
}
