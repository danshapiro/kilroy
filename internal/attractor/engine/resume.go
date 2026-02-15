package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
	"github.com/danshapiro/kilroy/internal/attractor/modeldb"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/cxdb"
)

var restartSuffixRE = regexp.MustCompile(`^restart-(\d+)$`)

type manifest struct {
	RunID         string            `json:"run_id"`
	RepoPath      string            `json:"repo_path"`
	RunBranch     string            `json:"run_branch"`
	RunConfigPath string            `json:"run_config_path"`
	ForceModels   map[string]string `json:"force_models"`

	ModelDB struct {
		OpenRouterModelInfoPath   string `json:"openrouter_model_info_path"`
		OpenRouterModelInfoSHA256 string `json:"openrouter_model_info_sha256"`
		OpenRouterModelInfoSource string `json:"openrouter_model_info_source"`
	} `json:"modeldb"`

	CXDB struct {
		HTTPBaseURL      string `json:"http_base_url"`
		ContextID        string `json:"context_id"`
		HeadTurnID       string `json:"head_turn_id"`
		RegistryBundleID string `json:"registry_bundle_id"`
	} `json:"cxdb"`
}

type ResumeOverrides struct {
	CXDBHTTPBaseURL string
	CXDBContextID   string
}

// Resume continues an existing run from {logs_root}/checkpoint.json.
//
// v1 resume source of truth:
// - filesystem checkpoint.json (execution state)
// - stage status.json for last completed node (routing outcome)
// - git commit SHA from checkpoint (code state)
func Resume(ctx context.Context, logsRoot string) (*Result, error) {
	return resumeFromLogsRoot(ctx, logsRoot, ResumeOverrides{})
}

func resumeFromLogsRoot(ctx context.Context, logsRoot string, ov ResumeOverrides) (res *Result, err error) {
	logsRoot = strings.TrimSpace(logsRoot)
	if logsRoot == "" {
		return nil, fmt.Errorf("logs_root is required")
	}
	if absLogsRoot, absErr := filepath.Abs(logsRoot); absErr != nil {
		return nil, absErr
	} else {
		logsRoot = absLogsRoot
	}

	var (
		runID         string
		checkpointSHA string
		eng           *Engine
	)
	defer func() {
		if err == nil {
			return
		}
		if eng != nil {
			eng.persistFatalOutcome(ctx, err)
			return
		}
		if strings.TrimSpace(logsRoot) == "" || strings.TrimSpace(runID) == "" {
			return
		}
		final := runtime.FinalOutcome{
			Timestamp:         time.Now().UTC(),
			Status:            runtime.FinalFail,
			RunID:             runID,
			FinalGitCommitSHA: strings.TrimSpace(checkpointSHA),
			FailureReason:     strings.TrimSpace(err.Error()),
		}
		_ = final.Save(filepath.Join(logsRoot, "final.json"))
	}()

	m, err := loadManifest(filepath.Join(logsRoot, "manifest.json"))
	if err != nil {
		return nil, err
	}
	runID = strings.TrimSpace(m.RunID)
	cp, err := runtime.LoadCheckpoint(filepath.Join(logsRoot, "checkpoint.json"))
	if err != nil {
		return nil, err
	}
	if err := validateAbsoluteResumePaths(logsRoot, cp); err != nil {
		return nil, err
	}
	checkpointSHA = strings.TrimSpace(cp.GitCommitSHA)
	if strings.TrimSpace(cp.GitCommitSHA) == "" {
		return nil, fmt.Errorf("checkpoint missing git_commit_sha")
	}
	dotSource, err := os.ReadFile(filepath.Join(logsRoot, "graph.dot"))
	if err != nil {
		return nil, err
	}
	g, _, err := Prepare(dotSource)
	if err != nil {
		return nil, err
	}

	// Best-effort: load the snapshotted run config if present.
	cfgPath := strings.TrimSpace(m.RunConfigPath)
	if cfgPath == "" {
		cfgPath = filepath.Join(logsRoot, "run_config.json")
	}
	var cfg *RunConfigFile
	if _, err := os.Stat(cfgPath); err == nil {
		if loaded, err := LoadRunConfigFile(cfgPath); err == nil {
			cfg = loaded
		}
	}

	// If we have a run config, resume with the real codergen router and CXDB sink.
	var backend CodergenBackend = &SimulatedCodergenBackend{}
	var sink *CXDBSink
	var catalog *modeldb.Catalog
	var startup *CXDBStartupInfo
	if cfg != nil {
		// Resume MUST use the run's snapshotted catalog.
		snapshotPath := firstExistingPath(
			strings.TrimSpace(m.ModelDB.OpenRouterModelInfoPath),
			filepath.Join(logsRoot, "modeldb", "openrouter_models.json"),
		)
		if strings.TrimSpace(snapshotPath) == "" {
			return nil, fmt.Errorf("resume: missing per-run model catalog snapshot: %s", filepath.Join(logsRoot, "modeldb", "openrouter_models.json"))
		}
		cat, err := loadCatalogForRun(snapshotPath)
		if err != nil {
			return nil, err
		}
		catalog = cat
		backend, err = newResumeCodergenBackend(cfg, catalog)
		if err != nil {
			return nil, err
		}

		// Re-attach to the existing CXDB context head (metaspec required).
		baseURL := strings.TrimSpace(ov.CXDBHTTPBaseURL)
		if baseURL == "" {
			baseURL = strings.TrimSpace(cfg.CXDB.HTTPBaseURL)
		}
		if baseURL == "" {
			baseURL = strings.TrimSpace(m.CXDB.HTTPBaseURL)
		}
		contextID := strings.TrimSpace(ov.CXDBContextID)
		if contextID == "" {
			contextID = strings.TrimSpace(m.CXDB.ContextID)
		}
		if baseURL != "" && contextID != "" {
			cfgForCXDB := *cfg
			cfgForCXDB.CXDB.HTTPBaseURL = baseURL
			cxdbClient, bin, startupInfo, err := ensureCXDBReady(ctx, &cfgForCXDB, logsRoot, m.RunID)
			if err != nil {
				return nil, err
			}
			startup = startupInfo
			defer func() { _ = bin.Close() }()
			if startupInfo != nil {
				// Defer process shutdown after bin close is deferred so shutdown runs first (LIFO).
				defer func() { _ = startupInfo.shutdownManagedProcesses() }()
			}
			bundleID, bundle, _, err := cxdb.KilroyAttractorRegistryBundle()
			if err != nil {
				return nil, err
			}
			if _, err := cxdbClient.PublishRegistryBundle(ctx, bundleID, bundle); err != nil {
				return nil, err
			}
			ci, err := cxdbClient.GetContext(ctx, contextID)
			if err != nil {
				return nil, err
			}
			sink = NewCXDBSink(cxdbClient, bin, m.RunID, contextID, ci.HeadTurnID, bundleID)
		}
	}

	prefix := deriveRunBranchPrefix(m, cfg)
	opts := RunOptions{
		RepoPath:        m.RepoPath,
		RunID:           m.RunID,
		LogsRoot:        logsRoot,
		WorktreeDir:     filepath.Join(logsRoot, "worktree"),
		RunBranchPrefix: prefix,
		RequireClean:    resolveRequireClean(cfg),
		ForceModels:     normalizeForceModels(copyStringStringMap(m.ForceModels)),
	}
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(prefix) == "" {
		return nil, fmt.Errorf("resume: unable to derive run_branch_prefix from manifest/config")
	}
	eng = newBaseEngine(g, dotSource, opts)
	eng.RunConfig = cfg
	eng.CodergenBackend = backend
	eng.CXDB = sink
	eng.ModelCatalogSHA = func() string {
		if catalog == nil {
			return ""
		}
		return catalog.SHA256
	}()
	eng.ModelCatalogSource = strings.TrimSpace(m.ModelDB.OpenRouterModelInfoSource)
	eng.ModelCatalogPath = func() string {
		if catalog == nil {
			return ""
		}
		return catalog.Path
	}()
	if startup != nil {
		for _, w := range startup.Warnings {
			eng.Warn(w)
		}
	}
	eng.Context.ReplaceSnapshot(cp.ContextValues, cp.Logs)
	eng.baseLogsRoot, eng.restartCount = restoreRestartState(logsRoot, cp)
	eng.restartFailureSignatures = restoreRestartFailureSignatures(cp)
	eng.loopFailureSignatures = restoreLoopFailureSignatures(cp)
	eng.baseSHA = cp.GitCommitSHA
	eng.lastCheckpointSHA = cp.GitCommitSHA
	if cp != nil && cp.Extra != nil {
		// Metaspec/attractor-spec: if the previous hop used `full` fidelity, degrade to
		// summary:high for the first resumed node unless exact session restore is supported.
		// Kilroy v1 does not serialize in-memory sessions, so always degrade.
		if strings.EqualFold(strings.TrimSpace(fmt.Sprint(cp.Extra["last_fidelity"])), "full") {
			eng.forceNextFidelity = "summary:high"
		}
	}

	if !gitutil.IsRepo(m.RepoPath) {
		return nil, fmt.Errorf("not a git repo: %s", m.RepoPath)
	}
	clean, err := gitutil.IsClean(m.RepoPath)
	if err != nil {
		return nil, err
	}
	if !clean {
		return nil, fmt.Errorf("repo has uncommitted changes (resume requires clean repo)")
	}

	// Recreate branch pointer and worktree at the last checkpoint commit.
	// The run branch may currently be checked out by the existing worktree at logs_root/worktree.
	// Remove it first so we can safely force-move the branch pointer.
	_ = gitutil.RemoveWorktree(m.RepoPath, eng.WorktreeDir)
	if err := gitutil.CreateBranchAt(m.RepoPath, eng.RunBranch, cp.GitCommitSHA); err != nil {
		return nil, err
	}
	if err := gitutil.AddWorktree(m.RepoPath, eng.WorktreeDir, eng.RunBranch); err != nil {
		return nil, err
	}
	if err := gitutil.ResetHard(eng.WorktreeDir, cp.GitCommitSHA); err != nil {
		return nil, err
	}

	// Re-run setup commands (e.g., npm install) since the recreated worktree
	// loses untracked artifacts produced by the original setup.
	if err := eng.executeSetupCommands(ctx); err != nil {
		return nil, fmt.Errorf("resume setup commands failed: %w", err)
	}

	// Determine next node to execute by re-evaluating routing from the last completed node.
	lastNodeID := strings.TrimSpace(cp.CurrentNode)
	if lastNodeID == "" {
		return nil, fmt.Errorf("checkpoint missing current_node")
	}
	lastStatusPath := filepath.Join(logsRoot, lastNodeID, "status.json")
	b, err := os.ReadFile(lastStatusPath)
	if err != nil {
		return nil, fmt.Errorf("read last status.json: %w", err)
	}
	lastOutcome, err := runtime.DecodeOutcomeJSON(b)
	if err != nil {
		return nil, fmt.Errorf("decode last status.json: %w", err)
	}

	// Reconstruct node outcomes for goal gate enforcement from completed nodes (best-effort).
	nodeOutcomes := map[string]runtime.Outcome{}
	for _, id := range cp.CompletedNodes {
		if id == "" {
			continue
		}
		sb, err := os.ReadFile(filepath.Join(logsRoot, id, "status.json"))
		if err != nil {
			continue
		}
		o, err := runtime.DecodeOutcomeJSON(sb)
		if err != nil {
			continue
		}
		nodeOutcomes[id] = o
	}

	// Kilroy v1: parallel nodes control the next hop via context.
	if lastNode := eng.Graph.Nodes[lastNodeID]; lastNode != nil {
		t := strings.TrimSpace(lastNode.TypeOverride())
		if t == "" {
			t = shapeToType(lastNode.Shape())
		}
		if t == "parallel" {
			join := strings.TrimSpace(eng.Context.GetString("parallel.join_node", ""))
			if join == "" {
				return nil, fmt.Errorf("resume: parallel node missing parallel.join_node in checkpoint context")
			}
			return eng.runLoop(ctx, join, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
		}
	}

	// Implicit fan-out: mirror forward-path logic for multi-edge convergence.
	allEdges, edgeErr := selectAllEligibleEdges(eng.Graph, lastNodeID, lastOutcome, eng.Context)
	if edgeErr != nil {
		return nil, edgeErr
	}
	if len(allEdges) > 1 {
		joinID, joinErr := findJoinNode(eng.Graph, allEdges)
		if joinErr == nil && joinID != "" {
			exec := &Execution{
				Graph:       eng.Graph,
				Context:     eng.Context,
				LogsRoot:    eng.LogsRoot,
				WorktreeDir: eng.WorktreeDir,
				Engine:      eng,
				Artifacts:   eng.Artifacts,
			}
			results, baseSHA, dispatchErr := dispatchParallelBranches(ctx, exec, lastNodeID, allEdges, joinID)
			if dispatchErr != nil {
				return nil, dispatchErr
			}
			stageDir := filepath.Join(eng.LogsRoot, lastNodeID)
			_ = os.MkdirAll(stageDir, 0o755)
			_ = writeJSON(filepath.Join(stageDir, "parallel_results.json"), results)

			eng.Context.ApplyUpdates(map[string]any{
				"parallel.join_node": joinID,
				"parallel.results":   results,
			})
			eng.appendProgress(map[string]any{
				"event":       "implicit_fan_out",
				"source_node": lastNodeID,
				"join_node":   joinID,
				"branches":    len(results),
				"base_sha":    baseSHA,
			})

			eng.incomingEdge = nil
			res, err = eng.runLoop(ctx, joinID, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
			if err != nil {
				return nil, err
			}
			if startup != nil {
				res.CXDBUIURL = strings.TrimSpace(startup.UIURL)
			}
			return res, nil
		}
	}

	nextHop, err := resolveNextHop(eng.Graph, lastNodeID, lastOutcome, eng.Context, classifyFailureClass(lastOutcome))
	if err != nil {
		return nil, err
	}
	if nextHop == nil || nextHop.Edge == nil {
		if lastOutcome.Status == runtime.StatusFail {
			// Mirror forward-path fallback: try the retry_target chain before dying.
			// Skip for fan-in nodes with deterministic failures — resolveNextHop
			// already considered retry_target and intentionally blocked it.
			fanInDeterministic := isFanInFailureLike(eng.Graph, lastNodeID, lastOutcome.Status) &&
				normalizedFailureClassOrDefault(classifyFailureClass(lastOutcome)) == failureClassDeterministic
			retryTarget := resolveRetryTarget(eng.Graph, lastNodeID)
			if retryTarget != "" && !fanInDeterministic {
				eng.appendProgress(map[string]any{
					"event":          "no_matching_fail_edge_fallback",
					"node_id":        lastNodeID,
					"retry_target":   retryTarget,
					"failure_reason": lastOutcome.FailureReason,
				})
				eng.incomingEdge = nil
				return eng.runLoop(ctx, retryTarget, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
			}
			return nil, fmt.Errorf("resume: stage failed with no outgoing fail edge: %s", strings.TrimSpace(lastOutcome.FailureReason))
		}
		// Nothing to do; treat as completed.
		return &Result{
			RunID:          eng.Options.RunID,
			LogsRoot:       eng.LogsRoot,
			WorktreeDir:    eng.WorktreeDir,
			RunBranch:      eng.RunBranch,
			FinalStatus:    runtime.FinalSuccess,
			FinalCommitSHA: cp.GitCommitSHA,
			Warnings:       eng.warningsCopy(),
			CXDBUIURL: func() string {
				if startup == nil {
					return ""
				}
				return strings.TrimSpace(startup.UIURL)
			}(),
		}, nil
	}
	nextEdge := nextHop.Edge

	// Continue traversal from next node.
	eng.incomingEdge = nextEdge
	res, err = eng.runLoop(ctx, nextEdge.To, append([]string{}, cp.CompletedNodes...), copyStringIntMap(cp.NodeRetries), nodeOutcomes)
	if err != nil {
		return nil, err
	}
	if startup != nil {
		res.CXDBUIURL = strings.TrimSpace(startup.UIURL)
	}
	return res, nil
}

func firstExistingPath(paths ...string) string {
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

func newResumeCodergenBackend(cfg *RunConfigFile, catalog *modeldb.Catalog) (CodergenBackend, error) {
	// Resume consumes snapshotted graph+config from a previously validated run,
	// so we only need runtime materialization here (not full preflight validation).
	runtimes, err := resolveProviderRuntimes(cfg)
	if err != nil {
		return nil, err
	}
	return NewCodergenRouterWithRuntimes(cfg, catalog, runtimes), nil
}

func loadManifest(path string) (*manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if strings.TrimSpace(m.RepoPath) == "" || strings.TrimSpace(m.RunBranch) == "" || strings.TrimSpace(m.RunID) == "" {
		return nil, fmt.Errorf("manifest missing required fields")
	}
	return &m, nil
}

func deriveRunBranchPrefix(m *manifest, cfg *RunConfigFile) string {
	if cfg != nil {
		if p := strings.TrimSpace(cfg.Git.RunBranchPrefix); p != "" {
			return p
		}
	}
	if m == nil {
		return ""
	}
	rb := strings.TrimSpace(m.RunBranch)
	rid := strings.TrimSpace(m.RunID)
	if rb != "" && rid != "" {
		suffix := "/" + rid
		if strings.HasSuffix(rb, suffix) {
			return strings.TrimSuffix(rb, suffix)
		}
	}
	return ""
}

func validateAbsoluteResumePaths(logsRoot string, cp *runtime.Checkpoint) error {
	if root := strings.TrimSpace(logsRoot); root != "" && !filepath.IsAbs(root) {
		return fmt.Errorf("resume: logs_root must be absolute: %s", root)
	}
	if cp == nil || cp.Extra == nil {
		return nil
	}
	if raw, ok := cp.Extra["base_logs_root"]; ok {
		if base := strings.TrimSpace(anyToStringValue(raw)); base != "" && !filepath.IsAbs(base) {
			return fmt.Errorf("resume: checkpoint base_logs_root must be absolute: %s", base)
		}
	}
	return nil
}

func restoreRestartState(logsRoot string, cp *runtime.Checkpoint) (string, int) {
	base := strings.TrimSpace(logsRoot)
	restarts := 0
	if cp != nil && cp.Extra != nil {
		if raw, ok := cp.Extra["base_logs_root"]; ok {
			if v := strings.TrimSpace(anyToStringValue(raw)); v != "" {
				base = v
			}
		}
		if raw, ok := cp.Extra["restart_count"]; ok {
			if n, ok := anyToNonNegativeInt(raw); ok {
				restarts = n
			}
		}
	}
	if m := restartSuffixRE.FindStringSubmatch(filepath.Base(logsRoot)); len(m) == 2 {
		if n, err := strconv.Atoi(m[1]); err == nil {
			if restarts == 0 || n > restarts {
				restarts = n
			}
			if base == logsRoot {
				base = filepath.Dir(logsRoot)
			}
		}
	}
	return base, restarts
}

func restoreRestartFailureSignatures(cp *runtime.Checkpoint) map[string]int {
	out := map[string]int{}
	if cp == nil || cp.Extra == nil {
		return out
	}
	raw, ok := cp.Extra["restart_failure_signatures"]
	if !ok || raw == nil {
		return out
	}
	switch m := raw.(type) {
	case map[string]int:
		for k, v := range m {
			if strings.TrimSpace(k) == "" || v < 0 {
				continue
			}
			out[strings.TrimSpace(k)] = v
		}
	case map[string]any:
		for k, v := range m {
			if strings.TrimSpace(k) == "" {
				continue
			}
			if n, ok := anyToNonNegativeInt(v); ok {
				out[strings.TrimSpace(k)] = n
			}
		}
	}
	return out
}

func restoreLoopFailureSignatures(cp *runtime.Checkpoint) map[string]int {
	out := map[string]int{}
	if cp == nil || cp.Extra == nil {
		return out
	}
	raw, ok := cp.Extra["loop_failure_signatures"]
	if !ok || raw == nil {
		return out
	}
	switch m := raw.(type) {
	case map[string]int:
		for k, v := range m {
			if strings.TrimSpace(k) == "" || v < 0 {
				continue
			}
			out[strings.TrimSpace(k)] = v
		}
	case map[string]any:
		for k, v := range m {
			if strings.TrimSpace(k) == "" {
				continue
			}
			if n, ok := anyToNonNegativeInt(v); ok {
				out[strings.TrimSpace(k)] = n
			}
		}
	}
	return out
}

func anyToStringValue(v any) string {
	if v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	default:
		s := strings.TrimSpace(fmt.Sprint(v))
		if s == "<nil>" {
			return ""
		}
		return s
	}
}

func anyToNonNegativeInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		if n >= 0 {
			return n, true
		}
	case int64:
		if n >= 0 {
			return int(n), true
		}
	case float64:
		if n >= 0 && n == float64(int(n)) {
			return int(n), true
		}
	case string:
		if i, err := strconv.Atoi(strings.TrimSpace(n)); err == nil && i >= 0 {
			return i, true
		}
	default:
		if s := strings.TrimSpace(fmt.Sprint(v)); s != "" && s != "<nil>" {
			if i, err := strconv.Atoi(s); err == nil && i >= 0 {
				return i, true
			}
		}
	}
	return 0, false
}
