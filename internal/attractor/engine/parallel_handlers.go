package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/gitutil"
	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
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
)

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

	joinID, err := findJoinFanInNode(exec.Graph, branches)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, nil
	}

	stageDir := filepath.Join(exec.LogsRoot, node.ID)
	_ = os.MkdirAll(stageDir, 0o755)

	// Kilroy git model: create the parallel node checkpoint commit FIRST so branch work is a descendant.
	// The parallel node itself is orchestration-only; its outcome is always SUCCESS unless orchestration fails.
	msg := fmt.Sprintf("attractor(%s): %s (%s)", exec.Engine.Options.RunID, node.ID, runtime.StatusSuccess)
	baseSHA, err := gitutil.CommitAllowEmpty(exec.WorktreeDir, msg)
	if err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, err
	}

	maxParallel := parseInt(node.Attr("max_parallel", ""), 4)
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
			res := h.runBranch(ctx, exec, node, baseSHA, joinID, j.idx, e, &gitMu)
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

	// Persist results as a stage artifact for easier inspection.
	_ = writeJSON(filepath.Join(stageDir, "parallel_results.json"), results)

	out := runtime.Outcome{
		Status: runtime.StatusSuccess,
		Notes:  fmt.Sprintf("parallel fan-out complete (%d branches), join=%s", len(results), joinID),
		ContextUpdates: map[string]any{
			"parallel.join_node": joinID,
			"parallel.results":   results,
		},
		Meta: map[string]any{
			"kilroy.git_checkpoint_sha": baseSHA,
		},
	}
	return out, nil
}

func (h *ParallelHandler) runBranch(ctx context.Context, exec *Execution, parallelNode *model.Node, baseSHA, joinID string, idx int, edge *model.Edge, gitMu *sync.Mutex) parallelBranchResult {
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
	branchName := buildParallelBranch(prefix, exec.Engine.Options.RunID, parallelNode.ID, key)
	branchRoot := filepath.Join(exec.LogsRoot, "parallel", parallelNode.ID, fmt.Sprintf("%02d-%s", idx+1, key))
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
		Graph:              exec.Graph,
		Options:            exec.Engine.Options,
		DotSource:          exec.Engine.DotSource,
		RunBranch:          branchName,
		WorktreeDir:        worktreeDir,
		LogsRoot:           branchRoot,
		Context:            exec.Context.Clone(),
		Registry:           exec.Engine.Registry,
		CodergenBackend:    exec.Engine.CodergenBackend,
		Interviewer:        exec.Engine.Interviewer,
		ModelCatalogSHA:    exec.Engine.ModelCatalogSHA,
		ModelCatalogSource: exec.Engine.ModelCatalogSource,
		ModelCatalogPath:   exec.Engine.ModelCatalogPath,
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
	emitBranchProgress("branch_subgraph_start", nil)
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

	return runtime.Outcome{
		Status: runtime.StatusSuccess,
		Notes:  fmt.Sprintf("fan-in selected %s (%s)", winner.BranchKey, winner.Outcome.Status),
		ContextUpdates: map[string]any{
			"parallel.fan_in.best_id":                winner.BranchKey,
			"parallel.fan_in.best_outcome":           winner.Outcome,
			"parallel.fan_in.best_head_sha":          winner.HeadSHA,
			"parallel.fan_in.best_cxdb_context_id":   winner.CXDBContextID,
			"parallel.fan_in.best_cxdb_head_turn_id": winner.CXDBHeadTurnID,
			"parallel.fan_in.losers":                 losers,
		},
	}, nil
}

type ManagerLoopHandler struct{}

func (h *ManagerLoopHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	_ = ctx
	_ = exec
	_ = node
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "stack.manager_loop not implemented in v1"}, nil
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
