package engine

import (
	"testing"

	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// --- parseJoinPolicy ---

func TestParseJoinPolicy(t *testing.T) {
	tests := []struct {
		input string
		want  joinPolicy
	}{
		{"wait_all", joinWaitAll},
		{"WAIT_ALL", joinWaitAll},
		{"first_success", joinFirstSuccess},
		{"First_Success", joinFirstSuccess},
		{"k_of_n", joinKOfN},
		{"K_OF_N", joinKOfN},
		{"quorum", joinQuorum},
		{"QUORUM", joinQuorum},
		{"", joinWaitAll},           // default
		{"unknown", joinWaitAll},    // default
		{"  wait_all  ", joinWaitAll}, // trimmed
	}
	for _, tc := range tests {
		got := parseJoinPolicy(tc.input)
		if got != tc.want {
			t.Errorf("parseJoinPolicy(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestParseErrorPolicy(t *testing.T) {
	tests := []struct {
		input string
		want  errorPolicy
	}{
		{"continue", errPolicyContinue},
		{"CONTINUE", errPolicyContinue},
		{"fail_fast", errPolicyFailFast},
		{"Fail_Fast", errPolicyFailFast},
		{"ignore", errPolicyIgnore},
		{"IGNORE", errPolicyIgnore},
		{"", errPolicyContinue},           // default
		{"unknown", errPolicyContinue},    // default
		{"  fail_fast  ", errPolicyFailFast}, // trimmed
	}
	for _, tc := range tests {
		got := parseErrorPolicy(tc.input)
		if got != tc.want {
			t.Errorf("parseErrorPolicy(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- parallelPolicies ---

func TestParallelPolicies(t *testing.T) {
	node := &model.Node{
		ID: "n1",
		Attrs: map[string]string{
			"join_policy":  "first_success",
			"error_policy": "fail_fast",
		},
	}
	jp, ep := parallelPolicies(node)
	if jp != joinFirstSuccess {
		t.Errorf("expected joinFirstSuccess, got %q", jp)
	}
	if ep != errPolicyFailFast {
		t.Errorf("expected errPolicyFailFast, got %q", ep)
	}
}

func TestParallelPolicies_Defaults(t *testing.T) {
	node := &model.Node{ID: "n2", Attrs: map[string]string{}}
	jp, ep := parallelPolicies(node)
	if jp != joinWaitAll {
		t.Errorf("expected joinWaitAll, got %q", jp)
	}
	if ep != errPolicyContinue {
		t.Errorf("expected errPolicyContinue, got %q", ep)
	}
}

// --- evaluateJoinPolicy: wait_all ---

func TestEvaluateJoinPolicy_WaitAll_AllSuccess(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
	}
	out := evaluateJoinPolicy(joinWaitAll, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_WaitAll_SomeFail(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinWaitAll, node, results)
	if out.Status != runtime.StatusPartialSuccess {
		t.Errorf("expected PARTIAL_SUCCESS, got %s", out.Status)
	}
	if out.FailureReason == "" {
		t.Error("expected non-empty FailureReason for partial success")
	}
}

func TestEvaluateJoinPolicy_WaitAll_PartialSuccessCountsAsSuccess(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusPartialSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusPartialSuccess}},
	}
	out := evaluateJoinPolicy(joinWaitAll, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS (PARTIAL_SUCCESS branches count as success), got %s", out.Status)
	}
}

// --- evaluateJoinPolicy: first_success ---

func TestEvaluateJoinPolicy_FirstSuccess_OneSucceeds(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
	}
	out := evaluateJoinPolicy(joinFirstSuccess, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_FirstSuccess_AllFail(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinFirstSuccess, node, results)
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
}

// --- evaluateJoinPolicy: k_of_n ---

func TestEvaluateJoinPolicy_KOfN_MeetsK(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"k": "2"}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinKOfN, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS, got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_KOfN_BelowK(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"k": "3"}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinKOfN, node, results)
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL, got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_KOfN_DefaultK1(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinKOfN, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS (k defaults to 1), got %s", out.Status)
	}
}

// --- evaluateJoinPolicy: quorum ---

func TestEvaluateJoinPolicy_Quorum_MetDefault50Percent(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "d", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinQuorum, node, results)
	if out.Status != runtime.StatusSuccess {
		t.Errorf("expected SUCCESS (2/4 >= 50%% quorum), got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_Quorum_BelowFraction(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"quorum_fraction": "0.75"}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "d", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	out := evaluateJoinPolicy(joinQuorum, node, results)
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL (1/4 < 75%% quorum), got %s", out.Status)
	}
}

func TestEvaluateJoinPolicy_Quorum_CustomFraction(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"quorum_fraction": "0.34"}}
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	// ceil(0.34 * 3) = ceil(1.02) = 2 => need 2 successes, only 1 => FAIL
	out := evaluateJoinPolicy(joinQuorum, node, results)
	if out.Status != runtime.StatusFail {
		t.Errorf("expected FAIL (1/3 < ceil(34%% of 3) = 2 quorum), got %s", out.Status)
	}
}

// --- filterResultsByErrorPolicy ---

func TestFilterResultsByErrorPolicy_Continue(t *testing.T) {
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	filtered := filterResultsByErrorPolicy(errPolicyContinue, results)
	if len(filtered) != 2 {
		t.Errorf("continue policy should return all results, got %d", len(filtered))
	}
}

func TestFilterResultsByErrorPolicy_FailFast(t *testing.T) {
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	filtered := filterResultsByErrorPolicy(errPolicyFailFast, results)
	if len(filtered) != 2 {
		t.Errorf("fail_fast policy should return all results, got %d", len(filtered))
	}
}

func TestFilterResultsByErrorPolicy_Ignore(t *testing.T) {
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusSuccess}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "c", Outcome: runtime.Outcome{Status: runtime.StatusPartialSuccess}},
	}
	filtered := filterResultsByErrorPolicy(errPolicyIgnore, results)
	if len(filtered) != 2 {
		t.Errorf("ignore policy should filter out failed results, got %d", len(filtered))
	}
	for _, r := range filtered {
		if r.Outcome.Status == runtime.StatusFail {
			t.Errorf("ignore policy should not include failed results, got branch %q", r.BranchKey)
		}
	}
}

func TestFilterResultsByErrorPolicy_Ignore_AllFail(t *testing.T) {
	results := []parallelBranchResult{
		{BranchKey: "a", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
		{BranchKey: "b", Outcome: runtime.Outcome{Status: runtime.StatusFail}},
	}
	filtered := filterResultsByErrorPolicy(errPolicyIgnore, results)
	if len(filtered) != 0 {
		t.Errorf("ignore policy with all failures should return empty, got %d", len(filtered))
	}
}

// --- parseFloat ---

func TestParseFloat(t *testing.T) {
	tests := []struct {
		input string
		def   float64
		want  float64
	}{
		{"0.5", 0.0, 0.5},
		{"0.75", 0.0, 0.75},
		{"", 0.5, 0.5},          // default
		{"invalid", 0.5, 0.5},   // default on error
		{"  1.0  ", 0.0, 1.0},   // trimmed
	}
	for _, tc := range tests {
		got := parseFloat(tc.input, tc.def)
		if got != tc.want {
			t.Errorf("parseFloat(%q, %.2f) = %.2f, want %.2f", tc.input, tc.def, got, tc.want)
		}
	}
}

// --- needsEarlyTermination ---

func TestNeedsEarlyTermination(t *testing.T) {
	tests := []struct {
		jp   joinPolicy
		ep   errorPolicy
		want bool
	}{
		{joinWaitAll, errPolicyContinue, false},
		{joinWaitAll, errPolicyIgnore, false},
		{joinWaitAll, errPolicyFailFast, true},
		{joinFirstSuccess, errPolicyContinue, true},
		{joinFirstSuccess, errPolicyFailFast, true},
		{joinKOfN, errPolicyContinue, true},
		{joinKOfN, errPolicyFailFast, true},
		{joinQuorum, errPolicyContinue, false},
	}
	for _, tc := range tests {
		got := needsEarlyTermination(tc.jp, tc.ep)
		if got != tc.want {
			t.Errorf("needsEarlyTermination(%q, %q) = %v, want %v", tc.jp, tc.ep, got, tc.want)
		}
	}
}

// --- earlyTerminationCheck ---

func TestEarlyTerminationCheck_FailFast_OnFailure(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	result := parallelBranchResult{
		BranchKey: "b1",
		Outcome:   runtime.Outcome{Status: runtime.StatusFail},
	}
	shouldCancel, reason := earlyTerminationCheck(joinWaitAll, errPolicyFailFast, node, result, 0, 1, 3)
	if !shouldCancel {
		t.Error("fail_fast should cancel on failure")
	}
	if reason == "" {
		t.Error("expected a reason")
	}
}

func TestEarlyTerminationCheck_FailFast_OnSuccess(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	result := parallelBranchResult{
		BranchKey: "b1",
		Outcome:   runtime.Outcome{Status: runtime.StatusSuccess},
	}
	shouldCancel, _ := earlyTerminationCheck(joinWaitAll, errPolicyFailFast, node, result, 1, 0, 3)
	if shouldCancel {
		t.Error("fail_fast should NOT cancel on success")
	}
}

func TestEarlyTerminationCheck_FirstSuccess_OnSuccess(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	result := parallelBranchResult{
		BranchKey: "b1",
		Outcome:   runtime.Outcome{Status: runtime.StatusSuccess},
	}
	shouldCancel, reason := earlyTerminationCheck(joinFirstSuccess, errPolicyContinue, node, result, 1, 0, 3)
	if !shouldCancel {
		t.Error("first_success should cancel on success")
	}
	if reason == "" {
		t.Error("expected a reason")
	}
}

func TestEarlyTerminationCheck_FirstSuccess_PartialSuccess(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	result := parallelBranchResult{
		BranchKey: "b1",
		Outcome:   runtime.Outcome{Status: runtime.StatusPartialSuccess},
	}
	shouldCancel, _ := earlyTerminationCheck(joinFirstSuccess, errPolicyContinue, node, result, 1, 0, 3)
	if !shouldCancel {
		t.Error("first_success should also cancel on partial success")
	}
}

func TestEarlyTerminationCheck_KOfN_ReachedK(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"k": "2"}}
	result := parallelBranchResult{
		BranchKey: "b3",
		Outcome:   runtime.Outcome{Status: runtime.StatusSuccess},
	}
	shouldCancel, _ := earlyTerminationCheck(joinKOfN, errPolicyContinue, node, result, 2, 0, 3)
	if !shouldCancel {
		t.Error("k_of_n should cancel when k successes reached")
	}
}

func TestEarlyTerminationCheck_KOfN_NotYetReachedK(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{"k": "3"}}
	result := parallelBranchResult{
		BranchKey: "b2",
		Outcome:   runtime.Outcome{Status: runtime.StatusSuccess},
	}
	shouldCancel, _ := earlyTerminationCheck(joinKOfN, errPolicyContinue, node, result, 1, 0, 3)
	if shouldCancel {
		t.Error("k_of_n should NOT cancel when k not yet reached")
	}
}

func TestEarlyTerminationCheck_NoPolicyTriggered(t *testing.T) {
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}
	result := parallelBranchResult{
		BranchKey: "b1",
		Outcome:   runtime.Outcome{Status: runtime.StatusSuccess},
	}
	shouldCancel, _ := earlyTerminationCheck(joinWaitAll, errPolicyContinue, node, result, 1, 0, 3)
	if shouldCancel {
		t.Error("wait_all+continue should never trigger early termination")
	}
}

// --- Zero-value result filtering ---

func TestEvaluateJoinPolicy_ZeroValueResultsNotCountedAsSuccess(t *testing.T) {
	// Simulate early termination: 3 branches, but only 1 completed (with failure).
	// The other 2 slots have zero-value BranchKey="" which means they were
	// cancelled/unscheduled. After filtering, only the completed branch should
	// remain, and evaluateJoinPolicy should see only the actual results.
	node := &model.Node{ID: "n1", Attrs: map[string]string{}}

	// Before the fix, these zero-value entries would flow into fan-in
	// where Status("") is treated as non-fail, producing a false success.
	allResults := []parallelBranchResult{
		{BranchKey: "branch_a", Outcome: runtime.Outcome{Status: runtime.StatusFail, FailureReason: "fail_fast triggered"}},
		{BranchKey: "", Outcome: runtime.Outcome{}}, // zero-value: cancelled branch
		{BranchKey: "", Outcome: runtime.Outcome{}}, // zero-value: unscheduled branch
	}

	// Filter out zero-value results (same logic as dispatchParallelBranchesStreaming).
	var populated []parallelBranchResult
	for _, r := range allResults {
		if r.BranchKey != "" {
			populated = append(populated, r)
		}
	}

	if len(populated) != 1 {
		t.Fatalf("expected 1 populated result, got %d", len(populated))
	}

	// With only the failed branch, first_success should report FAIL.
	out := evaluateJoinPolicy(joinFirstSuccess, node, populated)
	if out.Status != runtime.StatusFail {
		t.Errorf("first_success with only failed branches should be FAIL, got %s", out.Status)
	}

	// Without filtering, wait_all would see 0 failures in the 2 zero-value entries
	// and report SUCCESS — which is the bug this test guards against.
	outUnfiltered := evaluateJoinPolicy(joinWaitAll, node, allResults)
	if outUnfiltered.Status == runtime.StatusSuccess {
		t.Error("wait_all on unfiltered zero-value results should NOT report SUCCESS (zero-value entries have empty Status)")
	}
}
