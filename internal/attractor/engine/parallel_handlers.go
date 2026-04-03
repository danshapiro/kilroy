package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

type ParallelHandler struct{}

type parallelBranchResult struct {
	BranchKey      string              `json:"branch_key"`
	BranchName     string              `json:"branch_name"`
	StartNodeID    string              `json:"start_node_id"`
	StopNodeID     string              `json:"stop_node_id"`
	CXDBContextID  string              `json:"cxdb_context_id,omitempty"`
	CXDBHeadTurnID string              `json:"cxdb_head_turn_id,omitempty"`
	HeadSHA        string              `json:"head_sha"`
	LastNodeID     string              `json:"last_node_id"`
	Outcome        runtime.Outcome     `json:"outcome"`
	Completed      []string            `json:"completed_nodes"`
	LogsRoot       string              `json:"logs_root"`
	WorktreeDir    string              `json:"worktree_dir"`
	Error          string              `json:"error,omitempty"`
	Meta           map[string]any      `json:"meta,omitempty"`
	Context        map[string]any      `json:"context,omitempty"`
	Logs           []string            `json:"logs,omitempty"`
	DurationMS     int64               `json:"duration_ms,omitempty"`
	Artifacts      map[string][]string `json:"artifacts,omitempty"`
}

const (
	branchStaleWarningThreshold = 5 * time.Minute
	branchStaleWarningInterval  = 1 * time.Minute
	parallelMergeModeContextKey = "parallel.merge_mode"
	parallelMergeModeFanIn      = "fan_in_handler"
	parallelMergeModeManualBox  = "manual_box_fan_in"
)

func classifyJoinMergeMode(g *model.Graph, joinID string) string {
	if g == nil {
		return parallelMergeModeManualBox
	}
	joinNode := g.Nodes[strings.TrimSpace(joinID)]
	if joinNode == nil {
		return parallelMergeModeManualBox
	}
	t := strings.TrimSpace(joinNode.TypeOverride())
	if t == "" {
		t = shapeToType(joinNode.Shape())
	}
	if t == "parallel.fan_in" {
		return parallelMergeModeFanIn
	}
	return parallelMergeModeManualBox
}

func branchHeartbeatKeepaliveInterval(stallTimeout time.Duration) time.Duration {
	const (
		defaultInterval = 200 * time.Millisecond
		minInterval     = 50 * time.Millisecond
		maxInterval     = 2 * time.Second
	)
	if stallTimeout <= 0 {
		return defaultInterval
	}
	interval := stallTimeout / 3
	if interval < minInterval {
		return minInterval
	}
	if interval > maxInterval {
		return maxInterval
	}
	return interval
}

func eventFieldString(ev map[string]any, key string) string {
	if ev == nil {
		return ""
	}
	v, ok := ev[key]
	if !ok || v == nil {
		return ""
	}
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "" || s == "<nil>" {
		return ""
	}
	return s
}

func (h *ParallelHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	if exec == nil || exec.Engine == nil || exec.Graph == nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "parallel handler missing execution context"}, nil
	}

	branches := exec.Graph.Outgoing(node.ID)
	if len(branches) == 0 {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "parallel node has no outgoing edges"}, nil
	}

	// Allow explicit parallel fan-out nodes (shape=component) to converge on either:
	// - An explicit fan-in node (shape=tripleoctagon), or
	// - Any other topological convergence node (e.g. a consolidate box).
	//
	// This supports "map then reduce" patterns where the reducer node should
	// run once after all branches complete, without invoking FanInHandler
	// winner-selection behavior.
	joinID, err := findJoinNode(exec.Graph, branches)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}

	// Spec §4.8: read join_policy and error_policy from node attributes.
	jp, ep := parallelPolicies(node)

	// Spec §9.6: emit ParallelStarted CXDB event.
	parallelStart := time.Now()
	exec.Engine.cxdbParallelStarted(ctx, node.ID, len(branches),
		string(jp), string(ep))

	results, baseSHA, err := dispatchParallelBranchesWithPolicy(ctx, exec, node.ID, branches, joinID, jp, ep, node)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, err
	}

	// Spec §9.6: emit ParallelCompleted CXDB event.
	successCount, failCount := 0, 0
	for _, r := range results {
		if r.Outcome.Status == runtime.StatusSuccess || r.Outcome.Status == runtime.StatusPartialSuccess {
			successCount++
		} else if r.Outcome.Status == runtime.StatusFail {
			failCount++
		}
	}
	exec.Engine.cxdbParallelCompleted(ctx, node.ID, successCount, failCount,
		time.Since(parallelStart).Milliseconds())

	// Spec §4.8: apply error_policy=ignore to filter failed results BEFORE
	// join evaluation, so ignored failures don't affect join policy outcome.
	filteredResults := filterResultsByErrorPolicy(ep, results)

	// Spec §4.8: evaluate join_policy to determine aggregate outcome.
	policyOutcome := evaluateJoinPolicy(jp, node, filteredResults)

	// Use filtered results for context propagation to fan-in handler.
	contextResults := filteredResults

	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)
	_ = writeJSON(filepath.Join(stageDir, "parallel_results.json"), results)

	return runtime.Outcome{
		Status:        policyOutcome.Status,
		Notes:         fmt.Sprintf("parallel fan-out complete (%d branches), join=%s; %s", len(results), joinID, policyOutcome.Notes),
		FailureReason: policyOutcome.FailureReason,
		ContextUpdates: map[string]any{
			"parallel.join_node":        joinID,
			parallelMergeModeContextKey: classifyJoinMergeMode(exec.Graph, joinID),
			"parallel.results":          contextResults,
		},
		Meta: map[string]any{
			"kilroy.git_checkpoint_sha": baseSHA,
		},
	}, nil
}

// dispatchParallelBranches runs branches in parallel and returns the results.
// It creates a checkpoint commit, spawns worktrees for each branch, runs subgraphs,
// and collects results. This is the shared core used by both explicit ParallelHandler
// and implicit edge-topology fan-out.
func dispatchParallelBranches(
	ctx context.Context,
	exec *Execution,
	sourceNodeID string,
	branches []*model.Edge,
	joinID string,
) ([]parallelBranchResult, string, error) {
	if exec == nil || exec.Engine == nil || exec.Graph == nil {
		return nil, "", fmt.Errorf("dispatchParallelBranches: missing execution context")
	}
	if len(branches) == 0 {
		return nil, "", fmt.Errorf("dispatchParallelBranches: no branches")
	}

	// Resolve the source node for max_parallel and branch naming.
	sourceNode := exec.Graph.Nodes[sourceNodeID]
	if sourceNode == nil {
		// Create a minimal node so runBranch has something to reference.
		sourceNode = &model.Node{ID: sourceNodeID, Attrs: map[string]string{}}
	}

	// Kilroy git model: create the checkpoint commit FIRST so branch work is a descendant.
	msg := fmt.Sprintf("attractor(%s): %s (%s)", exec.Engine.Options.RunID, sourceNodeID, runtime.StatusSuccess)
	baseSHA, err := gitutil.CommitAllowEmpty(exec.WorktreeDir, msg)
	if err != nil {
		return nil, "", err
	}

	// Increment the per-node dispatch count so each pass through this fan-out
	// produces a unique branch name (pass1, pass2, …) that is independently
	// reviewable in git.
	passNum := exec.Engine.nextParallelPassCount(sourceNodeID)

	maxParallel := parseInt(sourceNode.Attr("max_parallel", ""), 4)
	if maxParallel <= 0 {
		maxParallel = 4
	}

	// git ref/worktree mutations are not concurrency-safe. Serialize setup operations,
	// then run branch execution concurrently.
	var gitMu sync.Mutex

	type job struct {
		idx  int
		edge *model.Edge
	}

	h := &ParallelHandler{}
	jobs := make(chan job)
	results := make([]parallelBranchResult, len(branches))
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for j := range jobs {
			e := j.edge
			if e == nil {
				continue
			}
			res := h.runBranch(ctx, exec, sourceNode, baseSHA, joinID, j.idx, e, passNum, &gitMu)
			results[j.idx] = res
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
	for idx, e := range branches {
		jobs <- job{idx: idx, edge: e}
	}
	close(jobs)
	wg.Wait()

	// Stable ordering for persistence and downstream fan-in evaluation.
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].BranchKey != results[j].BranchKey {
			return results[i].BranchKey < results[j].BranchKey
		}
		return results[i].StartNodeID < results[j].StartNodeID
	})

	return results, baseSHA, nil
}

func (h *ParallelHandler) runBranch(ctx context.Context, exec *Execution, parallelNode *model.Node, baseSHA, joinID string, idx int, edge *model.Edge, passNum int, gitMu *sync.Mutex) parallelBranchResult {
	key := sanitizeRefComponent(edge.To)
	if key == "" {
		key = fmt.Sprintf("branch-%d", idx+1)
	}
	prefix := strings.TrimSpace(exec.Engine.Options.RunBranchPrefix)
	if prefix == "" {
		msg := "parallel fan-out requires non-empty run_branch_prefix"
		return parallelBranchResult{
			BranchKey:   key,
			BranchName:  "",
			StartNodeID: edge.To,
			StopNodeID:  joinID,
			Error:       msg,
			Outcome: runtime.Outcome{
				Status:        runtime.StatusFail,
				FailureReason: msg,
			},
		}
	}

	// IMPORTANT: git ref namespace rules forbid creating refs under an existing ref path.
	// Since the main run branch is typically "attractor/run/<run_id>", parallel branches
	// MUST NOT be nested under that ref. Use a sibling namespace instead.
	// passNum is included so each re-visit of the fan-out node creates distinct
	// branches (pass1, pass2, …) that are independently reviewable in git.
	branchName := buildParallelBranch(prefix, exec.Engine.Options.RunID, parallelNode.ID, passNum, key)
	branchRoot := filepath.Join(exec.LogsRoot, "parallel", parallelNode.ID, fmt.Sprintf("pass%d", passNum), fmt.Sprintf("%02d-%s", idx+1, key))
	worktreeDir := filepath.Join(branchRoot, "worktree")
	var activityMu sync.Mutex
	lastProgressEvent := "branch_initialized"
	lastProgressAt := time.Now().UTC()
	lastStaleWarningAt := time.Time{}
	recordProgress := func(stage string, at time.Time) {
		activityMu.Lock()
		lastProgressEvent = stage
		lastProgressAt = at
		lastStaleWarningAt = time.Time{}
		activityMu.Unlock()
	}
	readActivity := func(now time.Time) (event string, at time.Time, idle time.Duration, warnedAt time.Time) {
		activityMu.Lock()
		event = lastProgressEvent
		at = lastProgressAt
		warnedAt = lastStaleWarningAt
		activityMu.Unlock()
		if at.IsZero() {
			at = now
		}
		idle = now.Sub(at)
		return event, at, idle, warnedAt
	}
	markStaleWarning := func(at time.Time) {
		activityMu.Lock()
		lastStaleWarningAt = at
		activityMu.Unlock()
	}
	emitBranchProgress := func(stage string, extra map[string]any) {
		now := time.Now().UTC()
		recordProgress(stage, now)
		ev := map[string]any{
			"event":            "branch_progress",
			"branch_key":       key,
			"branch_logs_root": branchRoot,
			"branch_event":     stage,
		}
		for k, v := range extra {
			ev[k] = v
		}
		exec.Engine.appendProgress(ev)
	}
	emitBranchHeartbeat := func() {
		now := time.Now().UTC()
		lastEvent, lastEventAt, idle, warnedAt := readActivity(now)
		exec.Engine.appendProgress(map[string]any{
			"event":                "branch_heartbeat",
			"branch_key":           key,
			"branch_logs_root":     branchRoot,
			"branch_last_event":    lastEvent,
			"branch_last_event_at": lastEventAt.Format(time.RFC3339Nano),
			"branch_idle_ms":       idle.Milliseconds(),
		})
		if idle < branchStaleWarningThreshold {
			return
		}
		if !warnedAt.IsZero() && now.Sub(warnedAt) < branchStaleWarningInterval {
			return
		}
		markStaleWarning(now)
		exec.Engine.appendProgress(map[string]any{
			"event":                "branch_stale_warning",
			"branch_key":           key,
			"branch_logs_root":     branchRoot,
			"branch_last_event":    lastEvent,
			"branch_last_event_at": lastEventAt.Format(time.RFC3339Nano),
			"branch_idle_ms":       idle.Milliseconds(),
			"stale_threshold_ms":   int64(branchStaleWarningThreshold / time.Millisecond),
		})
	}

	// Prepare branch git worktree rooted at the parallel node checkpoint commit.
	emitBranchProgress("branch_setup_start", nil)
	_ = os.MkdirAll(branchRoot, 0o755)
	if gitMu != nil {
		gitMu.Lock()
	}
	emitBranchProgress("branch_setup_locked", nil)
	_ = gitutil.RemoveWorktree(exec.Engine.Options.RepoPath, worktreeDir)
	if err := gitutil.CreateBranchAt(exec.Engine.Options.RepoPath, branchName, baseSHA); err != nil {
		if gitMu != nil {
			gitMu.Unlock()
		}
		return parallelBranchResult{
			BranchKey:   key,
			BranchName:  branchName,
			StartNodeID: edge.To,
			StopNodeID:  joinID,
			LogsRoot:    branchRoot,
			WorktreeDir: worktreeDir,
			Error:       err.Error(),
			Outcome:     runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()},
		}
	}
	if err := gitutil.AddWorktree(exec.Engine.Options.RepoPath, worktreeDir, branchName); err != nil {
		if gitMu != nil {
			gitMu.Unlock()
		}
		return parallelBranchResult{
			BranchKey:   key,
			BranchName:  branchName,
			StartNodeID: edge.To,
			StopNodeID:  joinID,
			LogsRoot:    branchRoot,
			WorktreeDir: worktreeDir,
			Error:       err.Error(),
			Outcome:     runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()},
		}
	}
	_ = gitutil.ResetHard(worktreeDir, baseSHA)
	if gitMu != nil {
		gitMu.Unlock()
	}
	emitBranchProgress("branch_setup_ready", nil)

	branchEng := &Engine{
		Graph:                      exec.Graph,
		Options:                    exec.Engine.Options,
		DotSource:                  exec.Engine.DotSource,
		RunBranch:                  branchName,
		WorktreeDir:                worktreeDir,
		LogsRoot:                   branchRoot,
		Context:                    exec.Context.Clone(),
		Registry:                   exec.Engine.Registry,
		AgentBackend:            exec.Engine.AgentBackend,
		Interviewer:                exec.Engine.Interviewer,
		ModelCatalogSHA:            exec.Engine.ModelCatalogSHA,
		ModelCatalogSource:         exec.Engine.ModelCatalogSource,
		ModelCatalogPath:           exec.Engine.ModelCatalogPath,
		InputMaterializationPolicy: exec.Engine.InputMaterializationPolicy,
		InputReferenceInferer:      exec.Engine.InputReferenceInferer,
		InputInferenceCache:        copyInferredReferenceCache(exec.Engine.InputInferenceCache),
		InputSourceTargetMap:       copyStringStringMap(exec.Engine.InputSourceTargetMap),
	}
	if exec.Engine.CXDB != nil {
		if fork, err := exec.Engine.CXDB.ForkFromHead(ctx); err == nil {
			branchEng.CXDB = fork
		}
	}
	branchEng.progressSink = func(ev map[string]any) {
		eventName := eventFieldString(ev, "event")
		if eventName == "" {
			return
		}
		extra := map[string]any{}
		if nodeID := eventFieldString(ev, "node_id"); nodeID != "" {
			extra["branch_node_id"] = nodeID
		}
		if status := eventFieldString(ev, "status"); status != "" {
			extra["branch_status"] = status
		}
		if reason := eventFieldString(ev, "failure_reason"); reason != "" {
			extra["branch_failure_reason"] = reason
		}
		if attempt, ok := ev["attempt"]; ok {
			extra["branch_attempt"] = attempt
		}
		if maxAttempts, ok := ev["max"]; ok {
			extra["branch_max"] = maxAttempts
		}
		if fromNode := eventFieldString(ev, "from_node"); fromNode != "" {
			extra["branch_from_node"] = fromNode
		}
		if toNode := eventFieldString(ev, "to_node"); toNode != "" {
			extra["branch_to_node"] = toNode
		}
		emitBranchProgress(eventName, extra)
	}
	if err := branchEng.materializeBranchStartupInputs(ctx, exec.WorktreeDir, exec.Engine.LogsRoot); err != nil {
		return parallelBranchResult{
			BranchKey:   key,
			BranchName:  branchName,
			StartNodeID: edge.To,
			StopNodeID:  joinID,
			LogsRoot:    branchRoot,
			WorktreeDir: worktreeDir,
			Error:       err.Error(),
			Outcome:     runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()},
		}
	}
	// Input materialization copies files from the parent worktree into the branch
	// worktree, which may overwrite the .git file with the parent's slot reference.
	// Repair the .git file so git operations in this branch use the correct slot.
	_ = gitutil.RepairWorktree(exec.Engine.Options.RepoPath, worktreeDir)
	// Copy gitignored files (e.g. .env, secrets, local configs) from the parent
	// worktree into the branch worktree. These are not committed to git and
	// therefore not present after worktree creation; agents need them without us
	// ever committing sensitive material.
	if err := gitutil.CopyIgnoredFiles(exec.WorktreeDir, worktreeDir); err != nil {
		emitBranchProgress("branch_ignored_files_warning", map[string]any{"warning": err.Error()})
	}
	if branchEng.CXDB != nil {
		if _, err := os.Stat(inputRunManifestPath(branchRoot)); err == nil {
			_, _ = branchEng.CXDB.PutArtifactFile(ctx, "", inputManifestFileName, inputRunManifestPath(branchRoot))
		}
	}
	emitBranchProgress("branch_subgraph_start", nil)
	// Spec §9.6: emit ParallelBranchStarted CXDB event.
	branchStart := time.Now()
	exec.Engine.cxdbParallelBranchStarted(ctx, parallelNode.ID, key, idx)
	keepaliveStop := make(chan struct{})
	keepaliveDone := make(chan struct{})
	keepaliveInterval := branchHeartbeatKeepaliveInterval(exec.Engine.Options.StallTimeout)
	go func() {
		defer close(keepaliveDone)
		ticker := time.NewTicker(keepaliveInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				emitBranchHeartbeat()
			case <-keepaliveStop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	res, err := runSubgraphUntil(ctx, branchEng, edge.To, joinID)
	close(keepaliveStop)
	<-keepaliveDone
	if err != nil {
		res.Error = err.Error()
		if res.Outcome.Status == "" {
			res.Outcome = runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}
		}
	}
	doneExtra := map[string]any{
		"branch_status": strings.TrimSpace(string(res.Outcome.Status)),
	}
	if reason := strings.TrimSpace(res.Outcome.FailureReason); reason != "" {
		doneExtra["branch_failure_reason"] = reason
	}
	emitBranchProgress("branch_subgraph_done", doneExtra)
	// Spec §9.6: emit ParallelBranchCompleted CXDB event.
	exec.Engine.cxdbParallelBranchCompleted(ctx, parallelNode.ID, key, idx,
		strings.TrimSpace(string(res.Outcome.Status)), time.Since(branchStart).Milliseconds())
	res.BranchKey = key
	res.BranchName = branchName
	res.StartNodeID = edge.To
	res.StopNodeID = joinID
	res.LogsRoot = branchRoot
	res.WorktreeDir = worktreeDir
	if branchEng.CXDB != nil {
		res.CXDBContextID = branchEng.CXDB.ContextID
		res.CXDBHeadTurnID = branchEng.CXDB.HeadTurnID
	}
	return res
}

type FanInHandler struct{}

func (h *FanInHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	raw, ok := exec.Context.Get("parallel.results")
	if !ok || raw == nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no parallel.results found in context"}, nil
	}

	results, err := decodeParallelResults(raw)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}
	if len(results) == 0 {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "no parallel results to evaluate"}, nil
	}

	winner, ok := selectHeuristicWinner(results)
	if !ok {
		failureClass := classifyParallelAllFailFailureClass(results)
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: "all parallel branches failed",
			Meta: map[string]any{
				"failure_class":     failureClass,
				"failure_signature": parallelAllFailSignature(results, failureClass),
			},
			ContextUpdates: map[string]any{
				"failure_class": failureClass,
			},
		}, nil
	}

	// Fast-forward the main run branch to the winner head.
	if strings.TrimSpace(winner.HeadSHA) != "" {
		if err := gitutil.FastForwardFFOnly(exec.WorktreeDir, winner.HeadSHA); err != nil {
			return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
		}
	}

	// Propagate git-ignored files from the winner branch worktree into the
	// main run worktree. FastForwardFFOnly only moves the git HEAD; it does
	// not touch untracked/ignored files. Without this, any .env / secrets /
	// build artifacts the winning branch worker wrote are silently dropped.
	// .ai/runs/ is excluded here: it is managed by the lineage system below
	// (mergeRunScopedFanInState) which merges it deterministically from all
	// branches according to promote_run_scoped patterns.
	if strings.TrimSpace(winner.WorktreeDir) != "" {
		if err := gitutil.CopyIgnoredFiles(winner.WorktreeDir, exec.WorktreeDir, ".ai/runs/"); err != nil {
			exec.Engine.appendProgress(map[string]any{
				"event":      "fan_in_ignored_files_warning",
				"node_id":    node.ID,
				"winner_key": winner.BranchKey,
				"warning":    err.Error(),
			})
		}
	}

	lineageRunHead := ""
	if exec != nil && exec.Engine != nil {
		var conflicts []InputSnapshotConflict
		var mergeErr error
		lineageRunHead, conflicts, mergeErr = exec.Engine.mergeRunScopedFanInState(results)
		if mergeErr != nil {
			if isInputSnapshotConflictError(mergeErr) {
				return runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: "input_snapshot_conflict",
					Meta: map[string]any{
						"conflicts":     conflictsToMeta(conflicts),
						"failure_class": failureClassDeterministic,
					},
					ContextUpdates: map[string]any{
						"failure_class": failureClassDeterministic,
					},
				}, nil
			}
			return runtime.Outcome{Status: runtime.StatusFail, FailureReason: mergeErr.Error()}, nil
		}
	}

	losers := []map[string]any{}
	for _, r := range results {
		if r.BranchKey == winner.BranchKey && r.HeadSHA == winner.HeadSHA {
			continue
		}
		losers = append(losers, map[string]any{
			"branch_key":        r.BranchKey,
			"branch_name":       r.BranchName,
			"head_sha":          r.HeadSHA,
			"status":            string(r.Outcome.Status),
			"logs_root":         r.LogsRoot,
			"cxdb_context_id":   r.CXDBContextID,
			"cxdb_head_turn_id": r.CXDBHeadTurnID,
		})
	}

	contextUpdates := map[string]any{
		"parallel.fan_in.best_id":                winner.BranchKey,
		"parallel.fan_in.best_outcome":           winner.Outcome,
		"parallel.fan_in.best_head_sha":          winner.HeadSHA,
		"parallel.fan_in.best_cxdb_context_id":   winner.CXDBContextID,
		"parallel.fan_in.best_cxdb_head_turn_id": winner.CXDBHeadTurnID,
		"parallel.fan_in.losers":                 losers,
	}
	if strings.TrimSpace(lineageRunHead) != "" {
		contextUpdates["input_lineage.run_head_revision"] = strings.TrimSpace(lineageRunHead)
	}

	return runtime.Outcome{
		Status:         runtime.StatusSuccess,
		Notes:          fmt.Sprintf("fan-in selected %s (%s)", winner.BranchKey, winner.Outcome.Status),
		ContextUpdates: contextUpdates,
	}, nil
}

// ManagerLoopHandler is defined in manager_loop.go.
type ManagerLoopHandler struct{}

type fanInBranchRevisionSource struct {
	RevisionID string
	LogsRoot   string
}

func (e *Engine) mergeRunScopedFanInState(results []parallelBranchResult) (string, []InputSnapshotConflict, error) {
	if e == nil || !e.inputMaterializationEnabled() {
		return "", nil, nil
	}
	promotePatterns := fanInPromoteRunScopedPatterns(e.RunConfig)
	if len(promotePatterns) == 0 {
		return "", nil, nil
	}
	if err := e.ensureLineageLoaded(); err != nil {
		return "", nil, err
	}
	if e.inputLineage == nil {
		return "", nil, fmt.Errorf("input snapshot lineage is not initialized")
	}

	runHeadBefore := strings.TrimSpace(e.inputLineage.RunHead)
	beforeDigest := map[string]string{}
	if runHeadBefore != "" {
		rev, ok := e.inputLineage.Revisions[runHeadBefore]
		if !ok {
			return "", nil, fmt.Errorf("unknown run head revision %q", runHeadBefore)
		}
		beforeDigest = normalizeDigestMap(rev.FileDigest)
	}

	branchRevs, branchSources, err := collectFanInBranchRevisions(results, e.inputLineage)
	if err != nil {
		return "", nil, err
	}
	if len(branchRevs) == 0 {
		return strings.TrimSpace(e.inputLineage.RunHead), nil, nil
	}

	newHead, conflicts, err := e.inputLineage.MergePromotedPaths(promotePatterns, branchRevs)
	if err != nil {
		return "", conflicts, err
	}
	newHead = strings.TrimSpace(newHead)
	if newHead == "" {
		return "", nil, fmt.Errorf("fan-in promotion merge produced empty run head")
	}

	if err := applyFanInPromotedPathsToRunScopedWorktree(e, beforeDigest, newHead, branchRevs, branchSources); err != nil {
		return "", nil, err
	}
	if err := persistRevisionSnapshot(e.LogsRoot, newHead, e.WorktreeDir, e.Options.RunID); err != nil {
		return "", nil, err
	}
	if err := e.inputLineage.SaveAtomic(e.LogsRoot); err != nil {
		return "", nil, err
	}
	return newHead, nil, nil
}

func fanInPromoteRunScopedPatterns(cfg *RunConfigFile) []string {
	if cfg == nil {
		return nil
	}
	return normalizePromotePatterns(cfg.Inputs.Materialize.FanIn.PromoteRunScoped)
}

func collectFanInBranchRevisions(
	results []parallelBranchResult,
	runLineage *InputSnapshotLineage,
) (map[string]string, map[string]fanInBranchRevisionSource, error) {
	ordered := append([]parallelBranchResult{}, results...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].BranchKey != ordered[j].BranchKey {
			return ordered[i].BranchKey < ordered[j].BranchKey
		}
		return ordered[i].StartNodeID < ordered[j].StartNodeID
	})

	branchRevs := map[string]string{}
	branchSources := map[string]fanInBranchRevisionSource{}
	for _, result := range ordered {
		branchKey := strings.TrimSpace(result.BranchKey)
		if branchKey == "" {
			continue
		}
		branchLogsRoot := strings.TrimSpace(result.LogsRoot)
		if branchLogsRoot == "" {
			continue
		}
		lineage, err := LoadInputSnapshotLineage(branchLogsRoot)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && result.Outcome.Status == runtime.StatusFail {
				continue
			}
			return nil, nil, fmt.Errorf("load branch lineage %q: %w", branchKey, err)
		}
		if lineage == nil {
			if result.Outcome.Status == runtime.StatusFail {
				continue
			}
			return nil, nil, fmt.Errorf("branch %q has empty lineage", branchKey)
		}
		revID := resolveFanInBranchHeadRevision(result, lineage)
		if revID == "" {
			if result.Outcome.Status == runtime.StatusFail {
				continue
			}
			return nil, nil, fmt.Errorf("branch %q missing head revision", branchKey)
		}
		rev, ok := lineage.Revisions[revID]
		if !ok {
			return nil, nil, fmt.Errorf("branch %q head revision %q missing from lineage", branchKey, revID)
		}
		if runLineage != nil {
			if runLineage.Revisions == nil {
				runLineage.Revisions = map[string]InputSnapshotRev{}
			}
			for id, branchRev := range lineage.Revisions {
				if _, exists := runLineage.Revisions[id]; exists {
					continue
				}
				runLineage.Revisions[id] = branchRev
			}
			runLineage.Revisions[revID] = rev
		}
		branchRevs[branchKey] = revID
		branchSources[branchKey] = fanInBranchRevisionSource{
			RevisionID: revID,
			LogsRoot:   branchLogsRoot,
		}
	}
	return branchRevs, branchSources, nil
}

func resolveFanInBranchHeadRevision(result parallelBranchResult, lineage *InputSnapshotLineage) string {
	if manifest, err := loadInputManifest(inputRunManifestPath(result.LogsRoot)); err == nil {
		if revID := strings.TrimSpace(manifest.BranchHeadRevision); revID != "" {
			return revID
		}
	}
	if lineage == nil {
		return ""
	}
	branchKey := strings.TrimSpace(result.BranchKey)
	if revID := strings.TrimSpace(lineage.BranchHeads[branchKey]); revID != "" {
		return revID
	}
	if len(lineage.BranchHeads) == 1 {
		for _, revID := range lineage.BranchHeads {
			if head := strings.TrimSpace(revID); head != "" {
				return head
			}
		}
	}
	for _, key := range sortedMapKeys(lineage.BranchHeads) {
		if revID := strings.TrimSpace(lineage.BranchHeads[key]); revID != "" {
			return revID
		}
	}
	return ""
}

func applyFanInPromotedPathsToRunScopedWorktree(
	e *Engine,
	beforeDigest map[string]string,
	newHead string,
	branchRevs map[string]string,
	branchSources map[string]fanInBranchRevisionSource,
) error {
	if e == nil || e.inputLineage == nil {
		return nil
	}
	rev, ok := e.inputLineage.Revisions[strings.TrimSpace(newHead)]
	if !ok {
		return fmt.Errorf("unknown merged run head revision %q", newHead)
	}
	afterDigest := normalizeDigestMap(rev.FileDigest)
	if len(afterDigest) == 0 {
		return nil
	}

	changed := make([]string, 0, len(afterDigest))
	for path, digest := range afterDigest {
		path = strings.TrimSpace(path)
		digest = strings.TrimSpace(digest)
		if path == "" || digest == "" {
			continue
		}
		if strings.TrimSpace(beforeDigest[path]) == digest {
			continue
		}
		changed = append(changed, path)
	}
	if len(changed) == 0 {
		return nil
	}
	sort.Strings(changed)

	runRoot := runScopedWorktreeRoot(e.WorktreeDir, e.Options.RunID)
	if err := os.MkdirAll(runRoot, 0o755); err != nil {
		return err
	}
	branchKeys := sortedMapKeys(branchRevs)
	for _, relPath := range changed {
		digest := strings.TrimSpace(afterDigest[relPath])
		srcFile := ""
		for _, branchKey := range branchKeys {
			revID := strings.TrimSpace(branchRevs[branchKey])
			if revID == "" {
				continue
			}
			branchRev, ok := e.inputLineage.Revisions[revID]
			if !ok {
				continue
			}
			if strings.TrimSpace(branchRev.FileDigest[relPath]) != digest {
				continue
			}
			sourceInfo, ok := branchSources[branchKey]
			if !ok {
				continue
			}
			candidate := filepath.Join(inputRevisionRoot(sourceInfo.LogsRoot, revID), filepath.FromSlash(relPath))
			if !isRegularFile(candidate) {
				continue
			}
			srcFile = candidate
			break
		}
		if srcFile == "" {
			return fmt.Errorf("promoted run-scoped path %q missing branch snapshot source", relPath)
		}
		dstFile := filepath.Join(runRoot, filepath.FromSlash(relPath))
		if err := copyInputFile(srcFile, dstFile); err != nil {
			return err
		}
	}
	return nil
}

func isInputSnapshotConflictError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	return strings.Contains(msg, "input_snapshot_conflict")
}

func conflictsToMeta(conflicts []InputSnapshotConflict) []map[string]any {
	if len(conflicts) == 0 {
		return nil
	}
	ordered := append([]InputSnapshotConflict{}, conflicts...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return strings.TrimSpace(ordered[i].Path) < strings.TrimSpace(ordered[j].Path)
	})

	out := make([]map[string]any, 0, len(ordered))
	for _, conflict := range ordered {
		pairs := append([]InputSnapshotConflictDigestPair{}, conflict.BranchDigests...)
		sort.SliceStable(pairs, func(i, j int) bool {
			if pairs[i].BranchKey != pairs[j].BranchKey {
				return pairs[i].BranchKey < pairs[j].BranchKey
			}
			return pairs[i].RevisionID < pairs[j].RevisionID
		})

		branches := make([]string, 0, len(pairs))
		metaPairs := make([]map[string]any, 0, len(pairs))
		for _, pair := range pairs {
			branchKey := strings.TrimSpace(pair.BranchKey)
			branches = append(branches, branchKey)
			metaPairs = append(metaPairs, map[string]any{
				"branch_key":  branchKey,
				"revision_id": strings.TrimSpace(pair.RevisionID),
				"digest":      strings.TrimSpace(pair.Digest),
			})
		}
		out = append(out, map[string]any{
			"path":           strings.TrimSpace(conflict.Path),
			"branches":       branches,
			"branch_digests": metaPairs,
		})
	}
	return out
}

func decodeParallelResults(raw any) ([]parallelBranchResult, error) {
	switch v := raw.(type) {
	case []parallelBranchResult:
		return v, nil
	case []any:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out []parallelBranchResult
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		return out, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		var out []parallelBranchResult
		if err := json.Unmarshal(b, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
}

func selectHeuristicWinner(results []parallelBranchResult) (parallelBranchResult, bool) {
	rank := func(s runtime.StageStatus) int {
		switch s {
		case runtime.StatusSuccess:
			return 0
		case runtime.StatusPartialSuccess:
			return 1
		case runtime.StatusRetry:
			return 2
		case runtime.StatusFail:
			return 3
		default:
			return 9
		}
	}
	// Candidates: at least one non-fail.
	cands := make([]parallelBranchResult, 0, len(results))
	for _, r := range results {
		if r.Outcome.Status != runtime.StatusFail {
			cands = append(cands, r)
		}
	}
	if len(cands) == 0 {
		return parallelBranchResult{}, false
	}
	sort.SliceStable(cands, func(i, j int) bool {
		ri := rank(cands[i].Outcome.Status)
		rj := rank(cands[j].Outcome.Status)
		if ri != rj {
			return ri < rj
		}
		if cands[i].BranchKey != cands[j].BranchKey {
			return cands[i].BranchKey < cands[j].BranchKey
		}
		return cands[i].HeadSHA < cands[j].HeadSHA
	})
	return cands[0], true
}

func classifyParallelAllFailFailureClass(results []parallelBranchResult) string {
	if len(results) == 0 {
		return failureClassDeterministic
	}
	for _, r := range results {
		cls := normalizedFailureClassOrDefault(readFailureClassHint(r.Outcome))
		if cls != failureClassTransientInfra {
			return failureClassDeterministic
		}
	}
	return failureClassTransientInfra
}

func parallelAllFailSignature(results []parallelBranchResult, failureClass string) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		reason := normalizeFailureReason(r.Outcome.FailureReason)
		if reason == "" {
			reason = "status=" + strings.ToLower(strings.TrimSpace(string(r.Outcome.Status)))
		}
		key := strings.TrimSpace(r.BranchKey)
		if key == "" {
			key = strings.TrimSpace(r.BranchName)
		}
		if key == "" {
			key = "unknown"
		}
		parts = append(parts, key+":"+reason)
	}
	sort.Strings(parts)
	sig := fmt.Sprintf(
		"parallel_all_failed|%s|branches=%d|%s",
		normalizedFailureClassOrDefault(failureClass),
		len(results),
		strings.Join(parts, ";"),
	)
	if len(sig) > 512 {
		sig = sig[:512]
	}
	return sig
}

func findJoinFanInNode(g *model.Graph, branches []*model.Edge) (string, error) {
	if g == nil {
		return "", fmt.Errorf("graph is nil")
	}
	if len(branches) == 0 {
		return "", fmt.Errorf("no branches")
	}

	type cand struct {
		id      string
		maxDist int
		sumDist int
	}

	reachable := make([]map[string]int, 0, len(branches))
	for _, e := range branches {
		if e == nil {
			continue
		}
		dists := bfsFanInDistances(g, e.To)
		reachable = append(reachable, dists)
	}
	if len(reachable) == 0 {
		return "", fmt.Errorf("no valid branches")
	}

	// Intersection of fan-in nodes reachable from all branches.
	cands := []cand{}
	for id, d0 := range reachable[0] {
		maxD := d0
		sumD := d0
		ok := true
		for i := 1; i < len(reachable); i++ {
			d, exists := reachable[i][id]
			if !exists {
				ok = false
				break
			}
			sumD += d
			if d > maxD {
				maxD = d
			}
		}
		if ok {
			cands = append(cands, cand{id: id, maxDist: maxD, sumDist: sumD})
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no parallel.fan_in join node reachable from all branches")
	}

	// Prefer closest join. Tie-break by lexical node id for determinism.
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].maxDist != cands[j].maxDist {
			return cands[i].maxDist < cands[j].maxDist
		}
		if cands[i].sumDist != cands[j].sumDist {
			return cands[i].sumDist < cands[j].sumDist
		}
		return cands[i].id < cands[j].id
	})
	return cands[0].id, nil
}

func bfsFanInDistances(g *model.Graph, start string) map[string]int {
	type item struct {
		id   string
		dist int
	}
	seen := map[string]bool{}
	queue := []item{{id: start, dist: 0}}
	seen[start] = true
	out := map[string]int{}

	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]

		n := g.Nodes[it.id]
		if n != nil && shapeToType(n.Shape()) == "parallel.fan_in" {
			// Record the first (shortest) distance.
			if _, exists := out[it.id]; !exists {
				out[it.id] = it.dist
			}
		}

		for _, e := range g.Outgoing(it.id) {
			if e == nil {
				continue
			}
			if seen[e.To] {
				continue
			}
			seen[e.To] = true
			queue = append(queue, item{id: e.To, dist: it.dist + 1})
		}
	}
	return out
}

// findJoinNode finds the convergence point for a set of branches.
// Prefers tripleoctagon (explicit fan-in) nodes. Falls back to any node
// reachable from ALL branches (topological convergence).
func findJoinNode(g *model.Graph, branches []*model.Edge) (string, error) {
	if g == nil {
		return "", fmt.Errorf("graph is nil")
	}
	if len(branches) == 0 {
		return "", fmt.Errorf("no branches")
	}

	// First, try the existing fan-in-only search — if a tripleoctagon exists, prefer it.
	joinID, err := findJoinFanInNode(g, branches)
	if err == nil && joinID != "" {
		return joinID, nil
	}

	// Fallback: find any convergence node reachable from all branches.
	type cand struct {
		id      string
		maxDist int
		sumDist int
	}

	reachable := make([]map[string]int, 0, len(branches))
	for _, e := range branches {
		if e == nil {
			continue
		}
		dists := bfsAllDistances(g, e.To)
		reachable = append(reachable, dists)
	}
	if len(reachable) == 0 {
		return "", fmt.Errorf("no valid branches")
	}

	// Intersection: nodes reachable from all branches.
	var cands []cand
	for id, d0 := range reachable[0] {
		maxD := d0
		sumD := d0
		ok := true
		for i := 1; i < len(reachable); i++ {
			d, exists := reachable[i][id]
			if !exists {
				ok = false
				break
			}
			sumD += d
			if d > maxD {
				maxD = d
			}
		}
		if ok {
			cands = append(cands, cand{id: id, maxDist: maxD, sumDist: sumD})
		}
	}
	if len(cands) == 0 {
		return "", fmt.Errorf("no convergence node reachable from all branches")
	}

	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].maxDist != cands[j].maxDist {
			return cands[i].maxDist < cands[j].maxDist
		}
		if cands[i].sumDist != cands[j].sumDist {
			return cands[i].sumDist < cands[j].sumDist
		}
		return cands[i].id < cands[j].id
	})
	return cands[0].id, nil
}

// bfsAllDistances returns distances from start to ALL reachable nodes (not just fan-in nodes).
func bfsAllDistances(g *model.Graph, start string) map[string]int {
	type item struct {
		id   string
		dist int
	}
	seen := map[string]bool{start: true}
	queue := []item{{id: start, dist: 0}}
	out := map[string]int{}

	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		// Record first (shortest) distance for every node except start itself.
		if it.id != start {
			if _, exists := out[it.id]; !exists {
				out[it.id] = it.dist
			}
		}
		for _, e := range g.Outgoing(it.id) {
			if e == nil || seen[e.To] {
				continue
			}
			seen[e.To] = true
			queue = append(queue, item{id: e.To, dist: it.dist + 1})
		}
	}
	return out
}

func sanitizeRefComponent(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return ""
	}
	return out
}

func copyInferredReferenceCache(in map[string][]InferredReference) map[string][]InferredReference {
	if len(in) == 0 {
		return map[string][]InferredReference{}
	}
	out := make(map[string][]InferredReference, len(in))
	for key, refs := range in {
		if len(refs) == 0 {
			out[key] = nil
			continue
		}
		cp := make([]InferredReference, len(refs))
		copy(cp, refs)
		out[key] = cp
	}
	return out
}
