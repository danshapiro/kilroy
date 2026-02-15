package engine

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// joinPolicy controls when the parallel handler considers its branches "done".
type joinPolicy string

// errorPolicy controls how the parallel handler reacts to branch failures.
type errorPolicy string

const (
	joinWaitAll      joinPolicy = "wait_all"
	joinFirstSuccess joinPolicy = "first_success"
	joinKOfN         joinPolicy = "k_of_n"
	joinQuorum       joinPolicy = "quorum"

	errPolicyContinue errorPolicy = "continue"
	errPolicyFailFast errorPolicy = "fail_fast"
	errPolicyIgnore   errorPolicy = "ignore"
)

func parseJoinPolicy(s string) joinPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "first_success":
		return joinFirstSuccess
	case "k_of_n":
		return joinKOfN
	case "quorum":
		return joinQuorum
	default:
		return joinWaitAll
	}
}

func parseErrorPolicy(s string) errorPolicy {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "fail_fast":
		return errPolicyFailFast
	case "ignore":
		return errPolicyIgnore
	default:
		return errPolicyContinue
	}
}

// parallelPolicies extracts join_policy and error_policy from node attributes.
func parallelPolicies(node *model.Node) (joinPolicy, errorPolicy) {
	jp := parseJoinPolicy(node.Attr("join_policy", ""))
	ep := parseErrorPolicy(node.Attr("error_policy", ""))
	return jp, ep
}

// evaluateJoinPolicy determines the aggregate outcome given branch results
// and the configured join policy. This runs after all branches complete
// (or after early termination for fail_fast/first_success).
func evaluateJoinPolicy(jp joinPolicy, node *model.Node, results []parallelBranchResult) runtime.Outcome {
	successCount := 0
	failCount := 0
	for _, r := range results {
		if r.Outcome.Status == runtime.StatusSuccess || r.Outcome.Status == runtime.StatusPartialSuccess {
			successCount++
		} else if r.Outcome.Status == runtime.StatusFail {
			failCount++
		}
	}
	total := len(results)

	switch jp {
	case joinWaitAll:
		if failCount == 0 {
			return runtime.Outcome{
				Status: runtime.StatusSuccess,
				Notes:  fmt.Sprintf("wait_all: all %d branches succeeded", total),
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusPartialSuccess,
			Notes:         fmt.Sprintf("wait_all: %d/%d branches failed", failCount, total),
			FailureReason: fmt.Sprintf("%d of %d branches failed", failCount, total),
		}

	case joinFirstSuccess:
		if successCount > 0 {
			return runtime.Outcome{
				Status: runtime.StatusSuccess,
				Notes:  fmt.Sprintf("first_success: %d succeeded", successCount),
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("first_success: all %d branches failed", total),
		}

	case joinKOfN:
		k := parseInt(node.Attr("k", ""), 1)
		if k <= 0 {
			k = 1
		}
		if successCount >= k {
			return runtime.Outcome{
				Status: runtime.StatusSuccess,
				Notes:  fmt.Sprintf("k_of_n: %d/%d succeeded (k=%d)", successCount, total, k),
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("k_of_n: only %d/%d succeeded (need k=%d)", successCount, total, k),
		}

	case joinQuorum:
		fraction := parseFloat(node.Attr("quorum_fraction", ""), 0.5)
		if fraction <= 0 {
			fraction = 0.5
		}
		if fraction > 1 {
			fraction = 1
		}
		needed := int(math.Ceil(float64(total) * fraction))
		if needed < 1 {
			needed = 1
		}
		if successCount >= needed {
			return runtime.Outcome{
				Status: runtime.StatusSuccess,
				Notes:  fmt.Sprintf("quorum: %d/%d succeeded (needed %d, fraction=%.2f)", successCount, total, needed, fraction),
			}
		}
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: fmt.Sprintf("quorum: only %d/%d succeeded (need %d, fraction=%.2f)", successCount, total, needed, fraction),
		}

	default:
		return runtime.Outcome{
			Status: runtime.StatusSuccess,
			Notes:  "default: all branches completed",
		}
	}
}

// filterResultsByErrorPolicy applies error_policy=ignore to filter out
// failed branch results before passing them to the fan-in handler.
func filterResultsByErrorPolicy(ep errorPolicy, results []parallelBranchResult) []parallelBranchResult {
	if ep != errPolicyIgnore {
		return results
	}
	filtered := make([]parallelBranchResult, 0, len(results))
	for _, r := range results {
		if r.Outcome.Status != runtime.StatusFail {
			filtered = append(filtered, r)
		}
	}
	return filtered
}

// parseFloat parses a float string with a default fallback.
func parseFloat(s string, def float64) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	if err != nil {
		return def
	}
	return f
}

// needsEarlyTermination returns true if the join/error policy combination
// requires checking results as they arrive (rather than waiting for all).
func needsEarlyTermination(jp joinPolicy, ep errorPolicy) bool {
	return ep == errPolicyFailFast || jp == joinFirstSuccess || jp == joinKOfN
}

// earlyTerminationCheck evaluates whether dispatch should be cancelled based
// on the policy and results received so far. Returns (shouldCancel, reason).
func earlyTerminationCheck(jp joinPolicy, ep errorPolicy, node *model.Node, result parallelBranchResult, successSoFar, failSoFar, total int) (bool, string) {
	// fail_fast: cancel on first failure
	if ep == errPolicyFailFast && result.Outcome.Status == runtime.StatusFail {
		return true, fmt.Sprintf("fail_fast: branch %q failed", result.BranchKey)
	}

	// first_success: cancel once we have a success
	if jp == joinFirstSuccess && (result.Outcome.Status == runtime.StatusSuccess || result.Outcome.Status == runtime.StatusPartialSuccess) {
		return true, fmt.Sprintf("first_success: branch %q succeeded", result.BranchKey)
	}

	// k_of_n: cancel once we have enough successes (optimization, not required)
	if jp == joinKOfN {
		k := parseInt(node.Attr("k", ""), 1)
		if k > 0 && successSoFar >= k {
			return true, fmt.Sprintf("k_of_n: reached k=%d successes", k)
		}
	}

	return false, ""
}

// dispatchParallelBranchesWithPolicy runs branches concurrently and supports
// early termination based on join_policy and error_policy. It creates a
// cancellable child context so that remaining branches can be cancelled when
// early termination triggers.
//
// For policies that need early termination (fail_fast, first_success), this
// uses a channel-based streaming pattern: branches send results as they
// complete, and the earlyTerminationCheck is evaluated after each result.
// When the check fires, the context is cancelled immediately so remaining
// branches see ctx.Done(). The function still waits for all goroutines to
// finish (via WaitGroup) to prevent goroutine leaks.
func dispatchParallelBranchesWithPolicy(
	ctx context.Context,
	exec *Execution,
	sourceNodeID string,
	branches []*model.Edge,
	joinID string,
	jp joinPolicy,
	ep errorPolicy,
	node *model.Node,
) ([]parallelBranchResult, string, error) {
	if !needsEarlyTermination(jp, ep) {
		// No early termination needed — use the standard dispatch path.
		return dispatchParallelBranches(ctx, exec, sourceNodeID, branches, joinID)
	}

	// For early termination policies, we use a streaming variant that sends
	// results over a channel as branches complete, allowing us to cancel
	// remaining branches as soon as the policy check fires.
	return dispatchParallelBranchesStreaming(ctx, exec, sourceNodeID, branches, joinID, jp, ep, node)
}

// dispatchParallelBranchesStreaming is the streaming variant of dispatchParallelBranches
// used for early termination policies. Each branch sends its result over a channel
// as it completes, and the caller evaluates earlyTerminationCheck after each result.
// When early termination triggers, the context is cancelled immediately; remaining
// branches observe ctx.Done() and fail fast. The function still waits for all
// goroutines to complete via WaitGroup to avoid leaks.
func dispatchParallelBranchesStreaming(
	ctx context.Context,
	exec *Execution,
	sourceNodeID string,
	branches []*model.Edge,
	joinID string,
	jp joinPolicy,
	ep errorPolicy,
	node *model.Node,
) ([]parallelBranchResult, string, error) {
	if exec == nil || exec.Engine == nil || exec.Graph == nil {
		return nil, "", fmt.Errorf("dispatchParallelBranchesStreaming: missing execution context")
	}
	if len(branches) == 0 {
		return nil, "", fmt.Errorf("dispatchParallelBranchesStreaming: no branches")
	}

	// Reuse the same setup as dispatchParallelBranches: resolve source node,
	// create checkpoint commit, set up git mutex and worker pool.
	sourceNode := exec.Graph.Nodes[sourceNodeID]
	if sourceNode == nil {
		sourceNode = &model.Node{ID: sourceNodeID, Attrs: map[string]string{}}
	}

	msg := fmt.Sprintf("attractor(%s): %s (%s)", exec.Engine.Options.RunID, sourceNodeID, runtime.StatusSuccess)
	baseSHA, err := gitutil.CommitAllowEmpty(exec.WorktreeDir, msg)
	if err != nil {
		return nil, "", err
	}

	maxParallel := parseInt(sourceNode.Attr("max_parallel", ""), 4)
	if maxParallel <= 0 {
		maxParallel = 4
	}

	var gitMu sync.Mutex

	type indexedResult struct {
		idx    int
		result parallelBranchResult
	}

	// Cancellable context for early termination.
	cancelCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	h := &ParallelHandler{}
	resultCh := make(chan indexedResult, len(branches))
	results := make([]parallelBranchResult, len(branches))

	type job struct {
		idx  int
		edge *model.Edge
	}
	jobs := make(chan job)
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			e := j.edge
			if e == nil {
				continue
			}
			res := h.runBranch(cancelCtx, exec, sourceNode, baseSHA, joinID, j.idx, e, &gitMu)
			resultCh <- indexedResult{idx: j.idx, result: res}
		}
	}

	workers := maxParallel
	if workers > len(branches) {
		workers = len(branches)
	}
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go worker()
	}

	// Feed jobs in a separate goroutine so we can read results concurrently.
	go func() {
		defer close(jobs)
		for idx, e := range branches {
			select {
			case jobs <- job{idx: idx, edge: e}:
			case <-cancelCtx.Done():
				// Context cancelled — stop sending new jobs.
				return
			}
		}
	}()

	// Close resultCh after all workers finish (in a separate goroutine).
	go func() {
		wg.Wait()
		close(resultCh)
	}()

	// Stream results and check early termination after each one.
	successSoFar := 0
	failSoFar := 0
	total := len(branches)
	received := 0
	terminated := false

	for ir := range resultCh {
		results[ir.idx] = ir.result
		received++

		if ir.result.Outcome.Status == runtime.StatusSuccess || ir.result.Outcome.Status == runtime.StatusPartialSuccess {
			successSoFar++
		} else if ir.result.Outcome.Status == runtime.StatusFail {
			failSoFar++
		}

		if !terminated {
			shouldCancel, reason := earlyTerminationCheck(jp, ep, node, ir.result, successSoFar, failSoFar, total)
			if shouldCancel {
				terminated = true
				exec.Engine.appendProgress(map[string]any{
					"event":        "early_termination",
					"node_id":      sourceNodeID,
					"reason":       reason,
					"received":     received,
					"total":        total,
					"success_so_far": successSoFar,
					"fail_so_far":   failSoFar,
				})
				cancel() // Cancel remaining branches immediately.
			}
		}
	}

	// Filter out zero-value results from cancelled/unscheduled branches.
	// When early termination fires, some branches never run — their slots
	// in the pre-allocated results slice stay at the zero value (BranchKey=="").
	// These placeholders must be removed before fan-in evaluation, otherwise
	// the zero-value Status("") is treated as non-fail and could be selected
	// as a "winner", producing a false success path.
	populated := make([]parallelBranchResult, 0, received)
	for _, r := range results {
		if r.BranchKey != "" {
			populated = append(populated, r)
		}
	}
	results = populated

	// Stable ordering for persistence and downstream fan-in evaluation.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].BranchKey != results[j].BranchKey {
			return results[i].BranchKey < results[j].BranchKey
		}
		return results[i].StartNodeID < results[j].StartNodeID
	})

	return results, baseSHA, nil
}
