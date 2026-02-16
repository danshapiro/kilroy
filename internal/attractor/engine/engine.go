package engine

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	rdebug "runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/cond"
	"github.com/danshapiro/kilroy/internal/attractor/dot"
	"github.com/danshapiro/kilroy/internal/attractor/gitutil"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
	"github.com/danshapiro/kilroy/internal/attractor/style"
	"github.com/danshapiro/kilroy/internal/attractor/validate"
)

type RunOptions struct {
	RepoPath string

	// RunID is a globally unique filesystem-safe identifier. If empty, one is generated (ULID).
	RunID string

	// LogsRoot defaults to:
	//   ${XDG_STATE_HOME:-$HOME/.local/state}/kilroy/attractor/runs/<run_id>
	LogsRoot string

	// WorktreeDir defaults to {LogsRoot}/worktree.
	WorktreeDir string

	// Git branch prefix defaults to "attractor/run".
	RunBranchPrefix string

	// If true (default), refuse to start when repo has uncommitted changes.
	RequireClean bool

	// Optional callback invoked after CXDB/UI bootstrap and before pipeline execution starts.
	// Pointer is used to avoid copying synchronization primitives inside CXDBStartupInfo.
	OnCXDBStartup func(info *CXDBStartupInfo)

	// Allows explicit opt-in for test-shim CLI execution profile.
	AllowTestShim bool

	// Optional provider-level model overrides (provider -> model id).
	// When set, the forced model is used for execution and bypasses model-catalog
	// membership validation for that provider.
	ForceModels map[string]string

	// Optional global stage timeout cap. When > 0, each stage attempt uses the
	// smaller positive timeout from node timeout and this global cap.
	StageTimeout time.Duration

	// Optional watchdog for no-progress stalls. Defaults are applied when unset.
	StallTimeout       time.Duration
	StallCheckInterval time.Duration

	// Optional cap for LLM retries in codergen routing.
	// Pointer preserves explicit zero versus unset semantics from config.
	MaxLLMRetries *int

	// Optional callback invoked for every progress event (same data written to
	// progress.ndjson). The map is a deep-copied snapshot safe for concurrent
	// use by the caller. Used by the HTTP server to fan events to SSE clients.
	ProgressSink func(map[string]any)

	// Optional interviewer for human-in-the-loop gates. Defaults to
	// AutoApproveInterviewer when nil.
	Interviewer Interviewer

	// Optional callback invoked after the engine is fully initialized but
	// before the main loop starts. Allows callers to capture an engine
	// reference for context inspection, etc.
	OnEngineReady func(e *Engine)
}

func (o *RunOptions) applyDefaults() error {
	if o.RunBranchPrefix == "" {
		o.RunBranchPrefix = "attractor/run"
	}
	// metaspec: require_clean defaults to true; an allow-dirty override is not required for v1.
	o.RequireClean = true
	if o.RunID == "" {
		id, err := NewRunID()
		if err != nil {
			return err
		}
		o.RunID = id
	}
	if o.LogsRoot == "" {
		o.LogsRoot = defaultLogsRoot(o.RunID)
	}
	if o.WorktreeDir == "" {
		o.WorktreeDir = filepath.Join(o.LogsRoot, "worktree")
	}
	// Runtime policy defaults (aligned with run config defaults).
	if o.StageTimeout < 0 {
		o.StageTimeout = 0
	}
	if o.StallTimeout < 0 {
		o.StallTimeout = 0
	}
	if o.StallCheckInterval < 0 {
		o.StallCheckInterval = 0
	}
	if o.MaxLLMRetries == nil {
		v := 6
		o.MaxLLMRetries = &v
	} else if *o.MaxLLMRetries < 0 {
		return fmt.Errorf("max llm retries must be >= 0")
	}
	o.ForceModels = normalizeForceModels(o.ForceModels)
	return nil
}

type Engine struct {
	Graph *model.Graph

	Options RunOptions

	// Original DOT input (pre-transforms), captured for replay/resume.
	DotSource []byte

	// Optional: config used to start the run (metaspec run config schema). Snapshotted to logs_root for resume.
	RunConfig *RunConfigFile

	RunBranch string

	WorktreeDir string
	LogsRoot    string

	Context *runtime.Context

	Registry *HandlerRegistry

	// Backend for codergen nodes (until provider routing is wired in).
	CodergenBackend CodergenBackend

	Interviewer Interviewer

	// Optional: normalized event sink (CXDB).
	CXDB *CXDBSink

	// Artifact store for the run (spec §5.5). Initialized once per run;
	// handlers access it via Execution.Artifacts.
	Artifacts *ArtifactStore

	// Model catalog snapshot metadata (metaspec).
	ModelCatalogSHA    string
	ModelCatalogSource string
	ModelCatalogPath   string

	warningsMu sync.Mutex
	Warnings   []string

	// loop_restart state (attractor-spec §3.2 Step 7).
	restartCount             int
	baseLogsRoot             string         // original LogsRoot before any restarts
	baseSHA                  string         // HEAD SHA at run start, needed for restart manifests
	restartFailureSignatures map[string]int // signature -> count across loop restarts
	lastCheckpointSHA        string
	terminalOutcomePersisted bool

	// Deterministic failure cycle detection: tracks failure signatures across
	// stages in the main loop. Never reset on success — signatures are keyed
	// by nodeID so a successful node cannot collide with a failing one, and
	// resetting would defeat the breaker in impl-succeeds/verify-fails cycles.
	loopFailureSignatures map[string]int

	progressMu sync.Mutex
	// Guarded by progressMu.
	lastProgressAt time.Time
	progressSink   func(map[string]any)

	// Fidelity/session resolution state.
	incomingEdge          *model.Edge // edge used to reach the current node (nil for start)
	forceNextFidelity     string      // non-empty => override resolved fidelity for the next LLM node
	forceNextFidelityUsed bool        // true once the override has been consumed
	lastResolvedFidelity  string      // last resolved LLM fidelity for checkpoint/resume
	lastResolvedThreadKey string      // thread key when fidelity=full (best-effort)
}

func (e *Engine) Warn(msg string) {
	if e == nil {
		return
	}
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return
	}
	e.warningsMu.Lock()
	e.Warnings = append(e.Warnings, msg)
	e.warningsMu.Unlock()
	e.appendProgress(map[string]any{
		"event":   "warning",
		"message": msg,
	})
}

func (e *Engine) warningsCopy() []string {
	if e == nil {
		return nil
	}
	e.warningsMu.Lock()
	defer e.warningsMu.Unlock()
	return append([]string{}, e.Warnings...)
}

type Result struct {
	RunID          string
	LogsRoot       string
	WorktreeDir    string
	RunBranch      string
	FinalStatus    runtime.FinalStatus
	FinalCommitSHA string
	Warnings       []string
	CXDBUIURL      string
}

type PrepareOptions struct {
	Transforms []Transform
	// RepoPath is the repository root directory. When set, prompt_file attributes
	// on nodes are resolved relative to this path before other transforms run.
	RepoPath string
	// KnownTypes is an optional list of handler type strings. When non-empty,
	// the TypeKnownRule lint rule is added to validation so that nodes with
	// explicit type= attributes not in this set produce a warning.
	KnownTypes []string
}

// Prepare parses/transforms/validates a graph.
func Prepare(dotSource []byte) (*model.Graph, []validate.Diagnostic, error) {
	return PrepareWithOptions(dotSource, PrepareOptions{})
}

func PrepareWithRegistry(dotSource []byte, reg *TransformRegistry) (*model.Graph, []validate.Diagnostic, error) {
	opts := PrepareOptions{}
	if reg != nil {
		opts.Transforms = reg.List()
	}
	return PrepareWithOptions(dotSource, opts)
}

func PrepareWithOptions(dotSource []byte, opts PrepareOptions) (*model.Graph, []validate.Diagnostic, error) {
	g, err := dot.Parse(dotSource)
	if err != nil {
		return nil, nil, err
	}

	// Built-in transforms: prompt_file resolution, stylesheet, $goal expansion.
	// prompt_file runs first so loaded content gets stylesheet defaults and $goal expansion.
	if opts.RepoPath != "" {
		if err := expandPromptFiles(g, opts.RepoPath); err != nil {
			return g, nil, fmt.Errorf("prompt_file expansion: %w", err)
		}
	}
	if raw := strings.TrimSpace(g.Attrs["model_stylesheet"]); raw != "" {
		rules, err := style.ParseStylesheet(raw)
		if err != nil {
			diags := []validate.Diagnostic{{
				Rule:     "stylesheet_syntax",
				Severity: validate.SeverityError,
				Message:  err.Error(),
			}}
			return g, diags, fmt.Errorf("stylesheet parse: %w", err)
		}
		_ = style.ApplyStylesheet(g, rules)
	}
	_ = (goalExpansionTransform{}).Apply(g)

	// Custom transforms run after built-ins, in registration order.
	for _, tr := range opts.Transforms {
		if tr == nil {
			continue
		}
		if err := tr.Apply(g); err != nil {
			return g, nil, fmt.Errorf("transform %s: %w", tr.ID(), err)
		}
	}

	// Spec §7.3: validate_or_raise collects ALL error-severity diagnostics
	// and reports them together, rather than returning on the first error.
	var extraRules []validate.LintRule
	if len(opts.KnownTypes) > 0 {
		extraRules = append(extraRules, validate.NewTypeKnownRule(opts.KnownTypes))
	}
	diags := validate.Validate(g, extraRules...)
	var errs []string
	for _, d := range diags {
		if d.Severity == validate.SeverityError {
			errs = append(errs, d.Rule+": "+d.Message)
		}
	}
	if len(errs) > 0 {
		return g, diags, fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}
	return g, diags, nil
}

// Run executes the pipeline in a dedicated git worktree and creates a checkpoint commit after each node.
func Run(ctx context.Context, dotSource []byte, opts RunOptions) (*Result, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	reg := NewDefaultRegistry()
	g, _, err := PrepareWithOptions(dotSource, PrepareOptions{
		RepoPath:   opts.RepoPath,
		KnownTypes: reg.KnownTypes(),
	})
	if err != nil {
		return nil, err
	}

	eng := newBaseEngine(g, dotSource, opts)
	eng.Registry = reg
	eng.CodergenBackend = &SimulatedCodergenBackend{}

	return eng.run(ctx)
}

func (e *Engine) run(ctx context.Context) (res *Result, err error) {
	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)

	defer func() {
		if err != nil {
			e.persistFatalOutcome(ctx, err)
		}
	}()

	if e.Options.RepoPath == "" {
		return nil, fmt.Errorf("repo.path is required")
	}
	if !gitutil.IsRepo(e.Options.RepoPath) {
		return nil, fmt.Errorf("not a git repo: %s", e.Options.RepoPath)
	}
	if e.Options.RequireClean {
		clean, err := gitutil.IsClean(e.Options.RepoPath)
		if err != nil {
			return nil, err
		}
		if !clean {
			return nil, fmt.Errorf("repo has uncommitted changes (require_clean=true)")
		}
	}

	baseSHA, err := gitutil.HeadSHA(e.Options.RepoPath)
	if err != nil {
		return nil, err
	}
	e.baseSHA = baseSHA
	if err := os.MkdirAll(e.LogsRoot, 0o755); err != nil {
		return nil, err
	}
	// Record PID so attractor status can detect a running process.
	_ = os.WriteFile(filepath.Join(e.LogsRoot, "run.pid"), []byte(strconv.Itoa(os.Getpid())), 0o644)
	// Snapshot the run config for repeatability and resume.
	if e.RunConfig != nil {
		_ = writeJSON(filepath.Join(e.LogsRoot, "run_config.json"), e.RunConfig)
	}

	// Create run branch at BASE_SHA and materialize a worktree for execution.
	if err := gitutil.CreateBranchAt(e.Options.RepoPath, e.RunBranch, baseSHA); err != nil {
		return nil, err
	}
	// If worktree exists (e.g., re-run), remove and recreate.
	_ = gitutil.RemoveWorktree(e.Options.RepoPath, e.WorktreeDir)
	if err := gitutil.AddWorktree(e.Options.RepoPath, e.WorktreeDir, e.RunBranch); err != nil {
		return nil, err
	}

	// Run metadata.
	if err := e.writeManifest(baseSHA); err != nil {
		return nil, err
	}
	// Persist the DOT input for replay/resume.
	if len(e.DotSource) > 0 {
		if err := os.WriteFile(filepath.Join(e.LogsRoot, "graph.dot"), e.DotSource, 0o644); err != nil {
			return nil, err
		}
	}
	if err := e.cxdbRunStarted(runCtx, baseSHA); err != nil {
		return nil, err
	}

	// Mirror graph attributes into context.
	for k, v := range e.Graph.Attrs {
		e.Context.Set("graph."+k, v)
	}
	e.Context.Set("graph.goal", e.Graph.Attrs["goal"])
	e.Context.Set("base_sha", baseSHA)

	// Expand $base_sha in prompts now that the base SHA is known.
	// ($goal was already expanded at parse/prepare time.)
	expandBaseSHA(e.Graph, baseSHA)

	// Run pre-pipeline setup commands (e.g., npm install) in the worktree.
	if err := e.executeSetupCommands(ctx); err != nil {
		return nil, fmt.Errorf("setup commands failed: %w", err)
	}

	// Capture the original logs root for loop_restart (attractor-spec §3.2 Step 7).
	e.baseLogsRoot = e.LogsRoot
	e.setLastProgressTime(time.Now().UTC())
	if e.Options.StallTimeout > 0 {
		checkEvery := e.Options.StallCheckInterval
		if checkEvery <= 0 {
			checkEvery = 5 * time.Second
		}
		go e.runStallWatchdog(runCtx, cancelRun, e.Options.StallTimeout, checkEvery)
	}

	current := findStartNodeID(e.Graph)
	if current == "" {
		return nil, fmt.Errorf("no start node found")
	}

	completed := []string{}
	nodeRetries := map[string]int{}

	// Node outcomes used for goal_gate checks.
	nodeOutcomes := map[string]runtime.Outcome{}

	return e.runLoop(runCtx, current, completed, nodeRetries, nodeOutcomes)
}

func (e *Engine) runLoop(ctx context.Context, current string, completed []string, nodeRetries map[string]int, nodeOutcomes map[string]runtime.Outcome) (*Result, error) {
	nodeVisits := map[string]int{}
	visitLimit := maxNodeVisits(e.Graph)
	for {
		if err := runContextError(ctx); err != nil {
			return nil, err
		}
		node := e.Graph.Nodes[current]
		if node == nil {
			return nil, fmt.Errorf("missing node: %s", current)
		}

		// Stuck-cycle detection: count how many times each node has been
		// visited in this iteration. When max_node_visits is set (>0) and a
		// node reaches that limit, halt — the pipeline is stuck in a retry
		// loop (e.g., implement succeeds but verify always fails due to
		// environment issues). This catches cycles that the signature-based
		// circuit breaker misses because the AI writes varying error messages.
		nodeVisits[current]++
		if visitLimit > 0 && nodeVisits[current] >= visitLimit {
			reason := fmt.Sprintf(
				"run aborted: node %q visited %d times in this iteration (limit %d); pipeline is stuck in a cycle",
				current, nodeVisits[current], visitLimit,
			)
			e.appendProgress(map[string]any{
				"event":       "stuck_cycle_breaker",
				"node_id":     current,
				"visit_count": nodeVisits[current],
				"visit_limit": visitLimit,
			})
			return nil, fmt.Errorf("%s", reason)
		}

		prev := ""
		if len(completed) > 0 {
			prev = completed[len(completed)-1]
		}
		e.Context.Set("previous_node", prev)
		e.Context.Set("current_node", current)
		e.Context.Set("completed_nodes", append([]string{}, completed...))

		// Spec §5.1: set built-in context key internal.retry_count.<node_id>.
		// Initialize to current retry count (0 on first visit, or restored value on resume).
		e.Context.Set(fmt.Sprintf("internal.retry_count.%s", current), nodeRetries[current])

		// Resolve fidelity/thread info for handlers that declare fidelity awareness
		// (e.g., LLM nodes) for checkpointing + resume semantics.
		if fa, ok := e.Registry.Resolve(node).(FidelityAwareHandler); ok && fa.UsesFidelity() {
			mode, threadKey := resolveFidelityAndThread(e.Graph, e.incomingEdge, node)
			if strings.TrimSpace(e.forceNextFidelity) != "" && !e.forceNextFidelityUsed {
				mode = strings.TrimSpace(e.forceNextFidelity)
				threadKey = ""
				if mode == "full" {
					threadKey = resolveThreadKey(e.Graph, e.incomingEdge, node)
				}
				e.forceNextFidelityUsed = true
			}
			e.lastResolvedFidelity = mode
			e.lastResolvedThreadKey = threadKey
		} else {
			e.lastResolvedFidelity = ""
			e.lastResolvedThreadKey = ""
		}

		if isTerminal(node) {
			ok, failedGate := checkGoalGates(e.Graph, nodeOutcomes)
			if !ok && failedGate != "" {
				retryTarget := resolveRetryTarget(e.Graph, failedGate)
				if retryTarget == "" {
					return nil, fmt.Errorf("goal gate unsatisfied (%s) and no retry target", failedGate)
				}
				e.incomingEdge = nil
				current = retryTarget
				continue
			}
			e.cxdbStageStarted(ctx, node)
			// Execute exit handler as the final checkpointed node.
			out, err := e.executeNode(ctx, node)
			if err != nil {
				return nil, err
			}
			nodeOutcomes[node.ID] = out
			completed = append(completed, node.ID)
			e.cxdbStageFinished(ctx, node, out)
			if err := runContextError(ctx); err != nil {
				return nil, err
			}
			sha, err := e.checkpoint(node.ID, out, completed, nodeRetries)
			if err != nil {
				return nil, err
			}
			e.lastCheckpointSHA = sha
			e.cxdbCheckpointSaved(ctx, node.ID, out.Status, sha)
			completionTurnID, err := e.cxdbRunCompleted(ctx, sha)
			if err != nil {
				return nil, err
			}
			final := runtime.FinalOutcome{
				Timestamp:         time.Now().UTC(),
				Status:            runtime.FinalSuccess,
				RunID:             e.Options.RunID,
				FinalGitCommitSHA: sha,
				CXDBContextID:     cxdbContextID(e.CXDB),
				CXDBHeadTurnID:    completionTurnID,
			}
			e.persistTerminalOutcome(ctx, final)
			return &Result{
				RunID:          e.Options.RunID,
				LogsRoot:       e.LogsRoot,
				WorktreeDir:    e.WorktreeDir,
				RunBranch:      e.RunBranch,
				FinalStatus:    runtime.FinalSuccess,
				FinalCommitSHA: sha,
				Warnings:       e.warningsCopy(),
			}, nil
		}

		e.cxdbStageStarted(ctx, node)
		out, err := e.executeWithRetry(ctx, node, nodeRetries)
		if err != nil {
			return nil, err
		}
		e.cxdbStageFinished(ctx, node, out)
		if err := runContextError(ctx); err != nil {
			return nil, err
		}

		// Record completion.
		completed = append(completed, node.ID)
		nodeOutcomes[node.ID] = out

		// Apply context updates and built-ins.
		e.Context.ApplyUpdates(out.ContextUpdates)
		e.Context.Set("outcome", string(out.Status))
		e.Context.Set("preferred_label", out.PreferredLabel)
		e.Context.Set("failure_reason", out.FailureReason)
		failureClass := classifyFailureClass(out)
		e.Context.Set("failure_class", failureClass)

		// Deterministic failure cycle detection: track failure signatures
		// across consecutive stages. On success, reset the tracker. On
		// deterministic failure, increment the signature count and abort
		// if the same signature has repeated too many times — this prevents
		// infinite loops when, e.g., a provider auth token expires and
		// every stage fails identically.
		if isFailureLoopRestartOutcome(out) && isSignatureTrackedFailureClass(failureClass) {
			sig := restartFailureSignature(node.ID, out, failureClass)
			if sig != "" {
				if e.loopFailureSignatures == nil {
					e.loopFailureSignatures = map[string]int{}
				}
				e.loopFailureSignatures[sig]++
				count := e.loopFailureSignatures[sig]
				limit := loopRestartSignatureLimit(e.Graph)
				e.appendProgress(map[string]any{
					"event":           "deterministic_failure_cycle_check",
					"node_id":         node.ID,
					"signature":       sig,
					"signature_count": count,
					"signature_limit": limit,
					"failure_class":   failureClass,
					"failure_reason":  out.FailureReason,
				})
				if count >= limit {
					reason := fmt.Sprintf(
						"run aborted: deterministic failure cycle detected — signature %q repeated %d times (limit %d); likely a persistent provider or auth error",
						sig, count, limit,
					)
					e.appendProgress(map[string]any{
						"event":           "deterministic_failure_cycle_breaker",
						"node_id":         node.ID,
						"signature":       sig,
						"signature_count": count,
						"signature_limit": limit,
					})
					return nil, fmt.Errorf("%s", reason)
				}
			}
		}

		// Checkpoint (git commit + checkpoint.json).
		sha, err := e.checkpoint(node.ID, out, completed, nodeRetries)
		if err != nil {
			return nil, err
		}
		e.lastCheckpointSHA = sha
		e.cxdbCheckpointSaved(ctx, node.ID, out.Status, sha)

		// Kilroy v1: explicit parallel nodes control the next hop via context.
		isExplicitParallel := false
		if t := strings.TrimSpace(node.TypeOverride()); t == "parallel" || (t == "" && shapeToType(node.Shape()) == "parallel") {
			isExplicitParallel = true
			join := strings.TrimSpace(e.Context.GetString("parallel.join_node", ""))
			if join == "" {
				return nil, fmt.Errorf("parallel node missing parallel.join_node in context")
			}
			e.incomingEdge = nil
			current = join
			continue
		}

		// Implicit fan-out: when a non-parallel node has multiple eligible outgoing
		// edges that converge at a common downstream node, dispatch them in parallel.
		if !isExplicitParallel {
			allEdges, edgeErr := selectAllEligibleEdges(e.Graph, node.ID, out, e.Context)
			if edgeErr != nil {
				return nil, edgeErr
			}
			if len(allEdges) > 1 {
				joinID, joinErr := findJoinNode(e.Graph, allEdges)
				if joinErr == nil && joinID != "" {
					exec := &Execution{
						Graph:       e.Graph,
						Context:     e.Context,
						LogsRoot:    e.LogsRoot,
						WorktreeDir: e.WorktreeDir,
						Engine:      e,
						Artifacts:   e.Artifacts,
					}
					results, baseSHA, dispatchErr := dispatchParallelBranches(ctx, exec, node.ID, allEdges, joinID)
					if dispatchErr != nil {
						return nil, dispatchErr
					}
					stageDir := filepath.Join(e.LogsRoot, node.ID)
					_ = os.MkdirAll(stageDir, 0o755)
					_ = writeJSON(filepath.Join(stageDir, "parallel_results.json"), results)

					e.Context.ApplyUpdates(map[string]any{
						"parallel.join_node": joinID,
						"parallel.results":   results,
					})
					e.appendProgress(map[string]any{
						"event":       "implicit_fan_out",
						"source_node": node.ID,
						"join_node":   joinID,
						"branches":    len(results),
						"base_sha":    baseSHA,
					})

					e.incomingEdge = nil
					current = joinID
					continue
				}
				// If no convergence node found, fall through to single-edge selection.
			}
		}

		// Resolve next hop with fan-in failure policy.
		nextHop, err := resolveNextHop(e.Graph, node.ID, out, e.Context, failureClass)
		if err != nil {
			return nil, err
		}
		if nextHop == nil || nextHop.Edge == nil {
			if out.Status == runtime.StatusFail {
				// Before dying, try the retry_target chain (same fallback as goal gates).
				// Skip for fan-in nodes with deterministic failures — resolveNextHop
				// already considered retry_target and intentionally blocked it to
				// prevent infinite loops where the same branches fail identically.
				fanInDeterministic := isFanInFailureLike(e.Graph, node.ID, out.Status) &&
					normalizedFailureClassOrDefault(failureClass) == failureClassDeterministic
				retryTarget := resolveRetryTarget(e.Graph, node.ID)
				if retryTarget != "" && !fanInDeterministic {
					e.appendProgress(map[string]any{
						"event":          "no_matching_fail_edge_fallback",
						"node_id":        node.ID,
						"retry_target":   retryTarget,
						"failure_reason": out.FailureReason,
					})
					e.incomingEdge = nil
					current = retryTarget
					continue
				}
				failedTurnID, _ := e.cxdbRunFailed(ctx, node.ID, sha, out.FailureReason)
				final := runtime.FinalOutcome{
					Timestamp:         time.Now().UTC(),
					Status:            runtime.FinalFail,
					RunID:             e.Options.RunID,
					FinalGitCommitSHA: sha,
					FailureReason:     out.FailureReason,
					CXDBContextID:     cxdbContextID(e.CXDB),
					CXDBHeadTurnID:    failedTurnID,
				}
				e.persistTerminalOutcome(ctx, final)
				return nil, fmt.Errorf("stage failed with no outgoing fail edge: %s", out.FailureReason)
			}
			completionTurnID, err := e.cxdbRunCompleted(ctx, sha)
			if err != nil {
				return nil, err
			}
			final := runtime.FinalOutcome{
				Timestamp:         time.Now().UTC(),
				Status:            runtime.FinalSuccess,
				RunID:             e.Options.RunID,
				FinalGitCommitSHA: sha,
				CXDBContextID:     cxdbContextID(e.CXDB),
				CXDBHeadTurnID:    completionTurnID,
			}
			e.persistTerminalOutcome(ctx, final)
			return &Result{
				RunID:          e.Options.RunID,
				LogsRoot:       e.LogsRoot,
				WorktreeDir:    e.WorktreeDir,
				RunBranch:      e.RunBranch,
				FinalStatus:    runtime.FinalSuccess,
				FinalCommitSHA: sha,
				Warnings:       e.warningsCopy(),
			}, nil
		}
		next := nextHop.Edge
		e.appendProgress(map[string]any{
			"event":      "edge_selected",
			"from_node":  node.ID,
			"to_node":    next.To,
			"label":      next.Label(),
			"condition":  next.Condition(),
			"hop_source": string(nextHop.Source),
		})

		// loop_restart (attractor-spec §3.2 Step 7): terminate current run, re-launch
		// with a fresh log directory starting at the edge's target node.
		if strings.EqualFold(next.Attr("loop_restart", "false"), "true") {
			return e.loopRestart(ctx, next.To, node.ID, out, failureClass)
		}
		e.incomingEdge = next
		current = next.To
	}
}

// loopRestart implements attractor-spec §3.2 Step 7: terminate the current run iteration
// and re-launch with a fresh log directory, starting at the given target node.
// The worktree is preserved (code changes carry over); only per-node log directories are fresh.
func (e *Engine) loopRestart(ctx context.Context, targetNodeID string, fromNodeID string, out runtime.Outcome, failureClass string) (*Result, error) {
	if strings.TrimSpace(e.baseLogsRoot) == "" {
		return nil, fmt.Errorf("loop_restart: base logs root is empty (resume invariants not restored)")
	}

	if isFailureLoopRestartOutcome(out) {
		if !strings.EqualFold(strings.TrimSpace(failureClass), failureClassTransientInfra) {
			reason := fmt.Sprintf(
				"loop_restart blocked: failure_class=%s (requires %s), node=%s, failure_reason=%s",
				normalizedFailureClassOrDefault(failureClass),
				failureClassTransientInfra,
				strings.TrimSpace(fromNodeID),
				strings.TrimSpace(out.FailureReason),
			)
			e.appendProgress(map[string]any{
				"event":          "loop_restart_blocked",
				"target_node":    targetNodeID,
				"node_id":        fromNodeID,
				"failure_class":  normalizedFailureClassOrDefault(failureClass),
				"failure_reason": out.FailureReason,
			})
			return nil, fmt.Errorf("%s", reason)
		}

		signature := restartFailureSignature(fromNodeID, out, failureClass)
		if signature != "" {
			if e.restartFailureSignatures == nil {
				e.restartFailureSignatures = map[string]int{}
			}
			e.restartFailureSignatures[signature]++
			count := e.restartFailureSignatures[signature]
			limit := loopRestartSignatureLimit(e.Graph)
			e.appendProgress(map[string]any{
				"event":             "loop_restart_signature",
				"target_node":       targetNodeID,
				"node_id":           fromNodeID,
				"signature":         signature,
				"signature_count":   count,
				"signature_limit":   limit,
				"failure_reason":    out.FailureReason,
				"failure_class":     normalizedFailureClassOrDefault(failureClass),
				"restart_count":     e.restartCount,
				"current_logs_root": e.LogsRoot,
			})
			if count >= limit {
				reason := fmt.Sprintf(
					"loop_restart circuit breaker: failure signature repeated %d times (limit %d): %s",
					count,
					limit,
					signature,
				)
				e.appendProgress(map[string]any{
					"event":           "loop_restart_circuit_breaker",
					"target_node":     targetNodeID,
					"node_id":         fromNodeID,
					"signature":       signature,
					"signature_count": count,
					"signature_limit": limit,
				})
				return nil, fmt.Errorf("%s", reason)
			}
		}
	}

	e.restartCount++
	maxRestarts := parseInt(e.Graph.Attrs["max_restarts"], 50)
	if e.restartCount > maxRestarts {
		return nil, fmt.Errorf("loop_restart limit exceeded (%d restarts, max %d)", e.restartCount, maxRestarts)
	}

	// Best-effort push before starting fresh iteration so remote has completed work.
	e.gitPushIfConfigured()

	// Create a fresh log sub-directory for this iteration.
	newLogsRoot := filepath.Join(e.baseLogsRoot, fmt.Sprintf("restart-%d", e.restartCount))
	if err := os.MkdirAll(newLogsRoot, 0o755); err != nil {
		return nil, fmt.Errorf("loop_restart: create logs dir: %w", err)
	}

	persistKeyNames := loopRestartPersistKeyNames(e.Graph)
	e.appendProgress(map[string]any{
		"event":              "loop_restart",
		"restart_count":      e.restartCount,
		"target_node":        targetNodeID,
		"new_logs_root":      newLogsRoot,
		"retry_budget_reset": true,
		"persist_keys":       persistKeyNames,
	})

	// Switch to fresh logs; worktree stays the same.
	e.LogsRoot = newLogsRoot

	// Write run metadata into the restart directory so consumers find manifest.json.
	if err := e.writeManifest(e.baseSHA); err != nil {
		return nil, fmt.Errorf("loop_restart: write manifest: %w", err)
	}
	if e.RunConfig != nil {
		_ = writeJSON(filepath.Join(newLogsRoot, "run_config.json"), e.RunConfig)
	}
	// Persist graph.dot in the new logs dir for replay/resume.
	if len(e.DotSource) > 0 {
		_ = os.WriteFile(filepath.Join(newLogsRoot, "graph.dot"), e.DotSource, 0o644)
	}

	// NOTE: loopFailureSignatures is intentionally NOT reset across loop restarts.
	// If the same deterministic failure persists after a restart, the counter should
	// keep accumulating so the circuit breaker can still trip and prevent infinite loops.

	// Snapshot context keys that should persist across loop restarts. This allows
	// pipelines to accumulate state (e.g., completed feature lists, features to skip)
	// that survives the context reset. Keys are specified via the graph-level
	// loop_restart_persist_keys attribute (comma-separated).
	persistedValues := e.snapshotPersistKeys()

	// Reset context: start fresh with only graph-level attributes.
	e.Context = runtime.NewContext()
	for k, v := range e.Graph.Attrs {
		e.Context.Set("graph."+k, v)
	}
	e.Context.Set("graph.goal", e.Graph.Attrs["goal"])
	e.Context.Set("base_sha", e.baseSHA)

	// Restore persisted context keys from the previous iteration.
	for k, v := range persistedValues {
		e.Context.Set(k, v)
	}

	// Inject loop restart metadata so pipelines can track iteration state.
	e.Context.Set("loop_restart.iteration_count", e.restartCount)
	e.Context.Set("loop_restart.from_node", fromNodeID)

	// Reset fidelity state.
	e.incomingEdge = nil
	e.forceNextFidelity = ""
	e.forceNextFidelityUsed = false

	// Fresh loop state: retries are per-iteration and intentionally reset on loop_restart.
	return e.runLoop(ctx, targetNodeID, nil, map[string]int{}, map[string]runtime.Outcome{})
}

// snapshotPersistKeys extracts context values that should survive a loop_restart
// context reset. Keys are specified via the graph-level loop_restart_persist_keys
// attribute as a comma-separated list (e.g., "completed_features,skipped_features").
func (e *Engine) snapshotPersistKeys() map[string]any {
	if e == nil || e.Graph == nil || e.Context == nil {
		return nil
	}
	raw := strings.TrimSpace(e.Graph.Attrs["loop_restart_persist_keys"])
	if raw == "" {
		return nil
	}
	persisted := map[string]any{}
	for _, key := range strings.Split(raw, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if v, ok := e.Context.Get(key); ok {
			persisted[key] = v
		}
	}
	if len(persisted) == 0 {
		return nil
	}
	return persisted
}

func (e *Engine) executeNode(ctx context.Context, node *model.Node) (runtime.Outcome, error) {
	// Effective timeout uses the smaller positive timeout between node timeout
	// and global StageTimeout.
	if timeout := effectiveStageTimeout(node, e.Options.StageTimeout); timeout > 0 {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		ctx = cctx
	}

	h := e.Registry.Resolve(node)
	stageDir := filepath.Join(e.LogsRoot, node.ID)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()}, err
	}
	// Nodes may execute multiple times (retry policy, goal gates, manual restarts). If a previous
	// attempt left a status.json behind and the handler doesn't write a new one, we'd incorrectly
	// treat the stale file as authoritative. Clear it before each attempt.
	_ = os.Remove(filepath.Join(stageDir, "status.json"))
	var (
		out runtime.Outcome
		err error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Spec: handler panics must not crash the engine; treat as FAIL.
				stack := string(rdebug.Stack())
				_ = os.WriteFile(filepath.Join(stageDir, "panic.txt"), []byte(fmt.Sprintf("%v\n\n%s", r, stack)), 0o644)
				out = runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: fmt.Sprintf("panic: %v", r),
					Notes:         "handler panic recovered",
				}
				err = nil
			}
		}()

		out, err = h.Execute(ctx, &Execution{
			Graph:       e.Graph,
			Context:     e.Context,
			LogsRoot:    e.LogsRoot,
			WorktreeDir: e.WorktreeDir,
			Engine:      e,
			Artifacts:   e.Artifacts,
		}, node)
	}()
	if err != nil {
		// Preserve any metadata (failure_class, failure_signature) the handler
		// attached to the outcome. Only override Status and FailureReason.
		// Spec §4.12: handler exceptions are converted to FAIL, not RETRY.
		// The failure classification safety net (shouldRetryOutcome) will still
		// promote transient failures to retryable based on failure_class metadata.
		if cause := context.Cause(ctx); cause != nil && cause != context.Canceled && cause != context.DeadlineExceeded {
			err = cause
		}
		out.Status = runtime.StatusFail
		out.FailureReason = err.Error()
	}

	// If the handler (or external tool) wrote status.json, treat it as authoritative.
	if b, readErr := os.ReadFile(filepath.Join(stageDir, "status.json")); readErr == nil {
		if parsed, decErr := runtime.DecodeOutcomeJSON(b); decErr == nil {
			out = parsed
		}
	}
	out, cerr := out.Canonicalize()
	if cerr != nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: cerr.Error()}, cerr
	}
	if (out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry) && ctx.Err() != nil {
		if cause := context.Cause(ctx); cause != nil && cause != context.Canceled && cause != context.DeadlineExceeded {
			out.FailureReason = cause.Error()
		}
	}
	// Enrich timeout outcomes with diagnostic metadata so downstream consumers
	// know the node timed out (vs. crashed) and what state the worktree was left in.
	// This runs after status.json is read so it applies regardless of handler path.
	if (out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry) && ctx.Err() == context.DeadlineExceeded {
		if out.Meta == nil {
			out.Meta = map[string]any{}
		}
		out.Meta["timeout"] = true
		if timeout := effectiveStageTimeout(node, e.Options.StageTimeout); timeout > 0 {
			out.Meta["timeout_seconds"] = int(timeout.Seconds())
		}
		partial := e.harvestPartialStatus(stageDir, node)
		if partial != nil {
			out.Meta["partial_status"] = partial
			_ = writeJSON(filepath.Join(stageDir, "partial_status.json"), partial)
		}
	}

	// Ensure required fields are present.
	if out.ContextUpdates == nil {
		out.ContextUpdates = map[string]any{}
	}
	if out.SuggestedNextIDs == nil {
		out.SuggestedNextIDs = []string{}
	}
	// Enforce metaspec: failure_reason must be non-empty when status=fail|retry.
	// Don't abort the run for a contract violation; coerce into a spec-compliant outcome.
	if err := out.Validate(); err != nil {
		if (out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry) && strings.TrimSpace(out.FailureReason) == "" {
			out.FailureReason = err.Error()
		}
	}

	// Write status.json (canonical metaspec shape).
	_ = writeJSON(filepath.Join(stageDir, "status.json"), out)
	return out, nil
}

// harvestPartialStatus checks the worktree after a timeout to determine what
// state the node left behind. This is best-effort diagnostic data — it never
// blocks or fails the run.
func (e *Engine) harvestPartialStatus(stageDir string, node *model.Node) map[string]any {
	if e.WorktreeDir == "" {
		return nil
	}
	partial := map[string]any{
		"node_id":   node.ID,
		"harvested": true,
	}
	// Count files changed in worktree relative to HEAD.
	diffOut, err := exec.CommandContext(context.Background(), "git", "-C", e.WorktreeDir, "diff", "--name-only", "HEAD").Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(diffOut)), "\n")
		changed := 0
		for _, l := range lines {
			if strings.TrimSpace(l) != "" {
				changed++
			}
		}
		partial["files_changed"] = changed
	}
	return partial
}

func (e *Engine) executeWithRetry(ctx context.Context, node *model.Node, retries map[string]int) (runtime.Outcome, error) {
	// Handlers that implement SingleExecutionHandler with SkipRetry()=true are
	// pass-through routing points. Retrying them based on a prior stage's
	// FAIL/RETRY just burns retry budget and can create misleading "max retries
	// exceeded" failures. Execute exactly once.
	if se, ok := e.Registry.Resolve(node).(SingleExecutionHandler); ok && se.SkipRetry() {
		e.appendProgress(map[string]any{
			"event":   "stage_attempt_start",
			"node_id": node.ID,
			"attempt": 1,
			"max":     1,
		})
		out, _ := e.executeNode(ctx, node)
		e.appendProgress(map[string]any{
			"event":          "stage_attempt_end",
			"node_id":        node.ID,
			"attempt":        1,
			"max":            1,
			"status":         string(out.Status),
			"failure_reason": out.FailureReason,
		})
		return out, nil
	}

	// Spec §3.5: retry precedence is (1) node max_retries, (2) graph default_max_retry,
	// (3) built-in default of 3. Use -1 sentinel to distinguish "not set" from "explicitly 0"
	// so that max_retries=0 on a node genuinely means "no retries".
	maxRetries := parseInt(node.Attr("max_retries", ""), -1)
	if maxRetries < 0 {
		maxRetries = parseInt(e.Graph.Attrs["default_max_retry"], -1)
	}
	if maxRetries < 0 {
		maxRetries = 3 // built-in default per spec §2.5
	}
	maxAttempts := maxRetries + 1

	// --- Escalation setup ---
	escalationChain := parseEscalationModels(node.Attr("escalation_models", ""))
	rbe := retriesBeforeEscalation(e.Graph)
	origModel := node.Attrs["llm_model"]
	origProvider := node.Attrs["llm_provider"]
	defer func() {
		// Always restore original attrs, even on early return.
		node.Attrs["llm_model"] = origModel
		node.Attrs["llm_provider"] = origProvider
	}()
	escalationFailCount := 0 // consecutive escalatable failures on the current model
	escalationIdx := -1      // -1 = using default model; 0+ = index into escalationChain

	allowPartial := strings.EqualFold(node.Attr("allow_partial", "false"), "true")
	stageDir := filepath.Join(e.LogsRoot, node.ID)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		e.appendProgress(map[string]any{
			"event":   "stage_attempt_start",
			"node_id": node.ID,
			"attempt": attempt,
			"max":     maxAttempts,
		})
		out, _ := e.executeNode(ctx, node)
		e.appendProgress(map[string]any{
			"event":          "stage_attempt_end",
			"node_id":        node.ID,
			"attempt":        attempt,
			"max":            maxAttempts,
			"status":         string(out.Status),
			"failure_reason": out.FailureReason,
		})
		if ctx.Err() != nil {
			co := canceledOutcomeForRetry(ctx, out)
			fo, _ := co.Canonicalize()
			_ = writeJSON(filepath.Join(stageDir, "status.json"), fo)
			return fo, nil
		}
		if out.Status == runtime.StatusSuccess || out.Status == runtime.StatusPartialSuccess || out.Status == runtime.StatusSkipped {
			retries[node.ID] = 0
			return out, nil
		}

		// Spec §3.3: custom (non-canonical) outcomes are routing decisions, not failures.
		// If the outcome matches any outgoing edge condition, return it as-is so the
		// main loop's edge selection can route on it. Reference dotfiles
		// (consensus_task.dot, semport.dot) use this pattern for multi-way branching
		// from box nodes (e.g. outcome=needs_dod, outcome=process, outcome=port).
		if !out.Status.IsCanonical() && hasMatchingOutgoingCondition(e.Graph, node.ID, out, e.Context) {
			retries[node.ID] = 0
			return out, nil
		}

		failureClass := classifyFailureClass(out)
		// Spec §9.6: emit StageFailed CXDB event on failure.
		willRetry := false // updated below if retry is possible
		canRetry := false
		if attempt < maxAttempts {
			// Tool command nodes (shape=parallelogram) always retry when
			// max_retries is set — the user explicitly opted in. LLM/API
			// nodes use failure classification to gate retries.
			isToolNode := strings.TrimSpace(node.Attr("tool_command", "")) != ""
			if isToolNode {
				canRetry = out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry
			} else if shouldRetryOutcome(out, failureClass) {
				canRetry = true
				// Check if escalation applies (capability failures, not transient)
				if isEscalatableFailureClass(failureClass) && len(escalationChain) > 0 {
					escalationFailCount++
					if escalationFailCount > rbe && escalationIdx < len(escalationChain)-1 {
						prevProvider := node.Attrs["llm_provider"]
						prevModel := node.Attrs["llm_model"]
						escalationIdx++
						next := escalationChain[escalationIdx]
						node.Attrs["llm_model"] = next.Model
						node.Attrs["llm_provider"] = next.Provider
						escalationFailCount = 0
						e.appendProgress(map[string]any{
							"event":          "escalation_model_switch",
							"node_id":        node.ID,
							"attempt":        attempt,
							"from_provider":  prevProvider,
							"from_model":     prevModel,
							"to_provider":    next.Provider,
							"to_model":       next.Model,
							"escalation_idx": escalationIdx,
							"failure_class":  failureClass,
						})
					}
				}
				// For transient_infra: no model change, just retry same model.
			}
		}
		if canRetry {
			willRetry = true
		}
		// Spec §9.6: emit StageFailed CXDB event.
		e.cxdbStageFailed(ctx, node, out.FailureReason, willRetry, attempt)
		if canRetry {
			retries[node.ID]++
			// Spec §5.1: update built-in context key internal.retry_count.<node_id> on each retry.
			e.Context.Set(fmt.Sprintf("internal.retry_count.%s", node.ID), retries[node.ID])
			delay := backoffDelayForNode(e.Options.RunID, e.Graph, node, attempt)
			// Spec §9.6: emit StageRetrying CXDB event.
			e.cxdbStageRetrying(ctx, node, attempt+1, delay.Milliseconds())
			e.appendProgress(map[string]any{
				"event":     "stage_retry_sleep",
				"node_id":   node.ID,
				"attempt":   attempt,
				"delay_ms":  delay.Milliseconds(),
				"retries":   retries[node.ID],
				"max_retry": maxRetries,
			})
			if !sleepWithContext(ctx, delay) {
				co := canceledOutcomeForRetry(ctx, out)
				fo, _ := co.Canonicalize()
				_ = writeJSON(filepath.Join(stageDir, "status.json"), fo)
				return fo, nil
			}
			continue
		}
		if attempt < maxAttempts && (out.Status == runtime.StatusFail || out.Status == runtime.StatusRetry) {
			e.appendProgress(map[string]any{
				"event":          "stage_retry_blocked",
				"node_id":        node.ID,
				"attempt":        attempt,
				"max":            maxAttempts,
				"status":         string(out.Status),
				"failure_reason": out.FailureReason,
				"failure_class":  failureClass,
				"max_retry":      maxRetries,
			})
		}
		if allowPartial {
			po, _ := (runtime.Outcome{
				Status:        runtime.StatusPartialSuccess,
				Notes:         "retries exhausted, partial accepted",
				FailureReason: out.FailureReason,
			}).Canonicalize()
			// The last attempt likely wrote status.json as FAIL. Rewrite it to reflect the
			// returned partial_success outcome (metaspec).
			_ = writeJSON(filepath.Join(stageDir, "status.json"), po)
			return po, nil
		}
		if out.FailureReason == "" {
			out.FailureReason = "max retries exceeded"
		}
		out.Status = runtime.StatusFail
		fo, _ := out.Canonicalize()
		_ = writeJSON(filepath.Join(stageDir, "status.json"), fo)
		return fo, nil
	}
	return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "max retries exceeded"}, nil
}

func sleepWithContext(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func canceledOutcomeForRetry(ctx context.Context, out runtime.Outcome) runtime.Outcome {
	out.Status = runtime.StatusFail
	if cause := context.Cause(ctx); cause != nil && strings.TrimSpace(cause.Error()) != "" {
		out.FailureReason = strings.TrimSpace(cause.Error())
	} else if reason := strings.TrimSpace(out.FailureReason); reason != "" {
		out.FailureReason = reason
	}
	if strings.TrimSpace(out.FailureReason) == "" {
		if err := ctx.Err(); err != nil {
			out.FailureReason = strings.TrimSpace(err.Error())
		}
	}
	if strings.TrimSpace(out.FailureReason) == "" {
		out.FailureReason = "run canceled"
	}
	if out.ContextUpdates == nil {
		out.ContextUpdates = map[string]any{}
	}
	out.ContextUpdates["failure_class"] = failureClassCanceled
	if out.SuggestedNextIDs == nil {
		out.SuggestedNextIDs = []string{}
	}
	return out
}

func runContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return ctx.Err()
}

func (e *Engine) checkpoint(nodeID string, out runtime.Outcome, completed []string, retries map[string]int) (string, error) {
	msg := fmt.Sprintf("attractor(%s): %s (%s)", e.Options.RunID, nodeID, out.Status)
	sha := ""
	if out.Meta != nil {
		if v, ok := out.Meta["kilroy.git_checkpoint_sha"]; ok {
			sha = strings.TrimSpace(fmt.Sprint(v))
		}
	}
	if sha == "" {
		var err error
		sha, err = gitutil.CommitAllowEmptyWithExcludes(e.WorktreeDir, msg, e.checkpointExcludeGlobs())
		if err != nil {
			return "", err
		}
	} else {
		head, err := gitutil.HeadSHA(e.WorktreeDir)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(head) != sha {
			return "", fmt.Errorf("handler-provided checkpoint sha does not match HEAD (head=%s meta=%s)", head, sha)
		}
	}
	cp := runtime.NewCheckpoint()
	cp.Timestamp = time.Now().UTC()
	cp.CurrentNode = nodeID
	cp.CompletedNodes = append([]string{}, completed...)
	cp.NodeRetries = copyStringIntMap(retries)
	cp.ContextValues = e.Context.SnapshotValues()
	cp.Logs = e.Context.SnapshotLogs()
	cp.GitCommitSHA = sha
	if cp.Extra == nil {
		cp.Extra = map[string]any{}
	}
	cp.Extra["base_logs_root"] = e.baseLogsRoot
	cp.Extra["restart_count"] = e.restartCount
	if len(e.restartFailureSignatures) > 0 {
		cp.Extra["restart_failure_signatures"] = copyStringIntMap(e.restartFailureSignatures)
	}
	if len(e.loopFailureSignatures) > 0 {
		cp.Extra["loop_failure_signatures"] = copyStringIntMap(e.loopFailureSignatures)
	}
	if strings.TrimSpace(e.lastResolvedFidelity) != "" {
		cp.Extra["last_fidelity"] = e.lastResolvedFidelity
		if strings.TrimSpace(e.lastResolvedThreadKey) != "" {
			cp.Extra["last_thread_key"] = e.lastResolvedThreadKey
		}
	}
	if err := cp.Save(filepath.Join(e.LogsRoot, "checkpoint.json")); err != nil {
		return "", err
	}
	return sha, nil
}

func (e *Engine) checkpointExcludeGlobs() []string {
	if e == nil || e.RunConfig == nil {
		return nil
	}
	return append([]string{}, e.RunConfig.Git.CheckpointExcludeGlobs...)
}

func (e *Engine) writeManifest(baseSHA string) error {
	manifest := map[string]any{
		"run_id":     e.Options.RunID,
		"graph_name": e.Graph.Name,
		"goal":       e.Graph.Attrs["goal"],
		"base_sha":   baseSHA,
		"run_branch": e.RunBranch,
		"logs_root":  e.LogsRoot,
		"worktree":   e.WorktreeDir,
		"graph_dot":  filepath.Join(e.LogsRoot, "graph.dot"),
		"started_at": time.Now().UTC().Format(time.RFC3339Nano),
		"repo_path":  e.Options.RepoPath,
		"kilroy_v1":  true,
		"run_config_path": func() string {
			if e.RunConfig == nil {
				return ""
			}
			return filepath.Join(e.LogsRoot, "run_config.json")
		}(),
		"modeldb": map[string]any{
			"openrouter_model_info_path":   e.ModelCatalogPath,
			"openrouter_model_info_sha256": e.ModelCatalogSHA,
			"openrouter_model_info_source": e.ModelCatalogSource,
		},
		"cxdb": func() map[string]any {
			if e.CXDB == nil || e.CXDB.Client == nil {
				return map[string]any{}
			}
			return map[string]any{
				"http_base_url":      e.CXDB.Client.BaseURL,
				"context_id":         e.CXDB.ContextID,
				"head_turn_id":       e.CXDB.HeadTurnID,
				"registry_bundle_id": e.CXDB.BundleID,
			}
		}(),
	}
	if ws := e.warningsCopy(); len(ws) > 0 {
		manifest["warnings"] = ws
	}
	if len(e.Options.ForceModels) > 0 {
		manifest["force_models"] = copyStringStringMap(e.Options.ForceModels)
	}
	return writeJSON(filepath.Join(e.LogsRoot, "manifest.json"), manifest)
}

func (e *Engine) persistFatalOutcome(ctx context.Context, runErr error) {
	if e == nil || runErr == nil || e.terminalOutcomePersisted {
		return
	}

	reason := strings.TrimSpace(runErr.Error())
	nodeID := ""
	if e.Context != nil {
		nodeID = strings.TrimSpace(e.Context.GetString("current_node", ""))
	}
	sha := strings.TrimSpace(e.lastCheckpointSHA)
	if sha == "" {
		if wt := strings.TrimSpace(e.WorktreeDir); wt != "" {
			if got, err := gitutil.HeadSHA(wt); err == nil {
				sha = strings.TrimSpace(got)
			}
		}
	}
	if sha == "" {
		sha = strings.TrimSpace(e.baseSHA)
	}

	failedTurnID, _ := e.cxdbRunFailed(ctx, nodeID, sha, reason)
	final := runtime.FinalOutcome{
		Timestamp:         time.Now().UTC(),
		Status:            runtime.FinalFail,
		RunID:             e.Options.RunID,
		FinalGitCommitSHA: sha,
		FailureReason:     reason,
		CXDBContextID:     cxdbContextID(e.CXDB),
		CXDBHeadTurnID:    strings.TrimSpace(failedTurnID),
	}
	if final.CXDBHeadTurnID == "" && e.CXDB != nil {
		final.CXDBHeadTurnID = strings.TrimSpace(e.CXDB.HeadTurnID)
	}
	e.persistTerminalOutcome(ctx, final)
}

func (e *Engine) persistTerminalOutcome(ctx context.Context, final runtime.FinalOutcome) {
	if e == nil || e.terminalOutcomePersisted {
		return
	}
	if final.Timestamp.IsZero() {
		final.Timestamp = time.Now().UTC()
	}
	if strings.TrimSpace(final.RunID) == "" {
		final.RunID = strings.TrimSpace(e.Options.RunID)
	}
	if strings.TrimSpace(final.CXDBContextID) == "" {
		final.CXDBContextID = cxdbContextID(e.CXDB)
	}
	if strings.TrimSpace(final.CXDBHeadTurnID) == "" && e.CXDB != nil {
		final.CXDBHeadTurnID = strings.TrimSpace(e.CXDB.HeadTurnID)
	}

	primaryPath := ""
	for _, p := range e.finalOutcomePaths() {
		if err := final.Save(p); err != nil {
			continue
		}
		if primaryPath == "" {
			primaryPath = p
		}
	}
	if primaryPath == "" {
		root := strings.TrimSpace(e.LogsRoot)
		if root == "" {
			root = strings.TrimSpace(e.baseLogsRoot)
		}
		if root != "" {
			primaryPath = filepath.Join(root, "final.json")
			_ = final.Save(primaryPath)
		}
	}
	if e.CXDB != nil && strings.TrimSpace(primaryPath) != "" {
		_, _ = e.CXDB.PutArtifactFile(ctx, "", "final.json", primaryPath)
	}

	archiveRoot := strings.TrimSpace(e.LogsRoot)
	if archiveRoot != "" {
		runTar := filepath.Join(archiveRoot, "run.tgz")
		_ = writeTarGz(runTar, archiveRoot, includeInRunArchive)
		if e.CXDB != nil {
			if _, err := os.Stat(runTar); err == nil {
				_, _ = e.CXDB.PutArtifactFile(ctx, "", "run.tgz", runTar)
			}
		}
	}

	e.terminalOutcomePersisted = true

	// Best-effort push after terminal outcome so remote has final state.
	e.gitPushIfConfigured()
}

// gitPushIfConfigured pushes the run branch to the configured remote.
// It is best-effort: failures are logged as warnings but never abort the run.
func (e *Engine) gitPushIfConfigured() {
	if e == nil || e.RunConfig == nil {
		return
	}
	remote := strings.TrimSpace(e.RunConfig.Git.PushRemote)
	if remote == "" {
		return
	}
	branch := strings.TrimSpace(e.RunBranch)
	if branch == "" {
		return
	}
	repoDir := strings.TrimSpace(e.Options.RepoPath)
	if repoDir == "" {
		return
	}
	e.appendProgress(map[string]any{
		"event":  "git_push_start",
		"remote": remote,
		"branch": branch,
	})
	if err := gitutil.PushBranch(repoDir, remote, branch); err != nil {
		e.Warn(fmt.Sprintf("git push %s %s: %v", remote, branch, err))
		e.appendProgress(map[string]any{
			"event":  "git_push_failed",
			"remote": remote,
			"branch": branch,
			"error":  err.Error(),
		})
		return
	}
	e.appendProgress(map[string]any{
		"event":  "git_push_ok",
		"remote": remote,
		"branch": branch,
	})
}

func (e *Engine) finalOutcomePaths() []string {
	if e == nil {
		return nil
	}
	out := []string{}
	seen := map[string]bool{}
	add := func(root string) {
		root = strings.TrimSpace(root)
		if root == "" {
			return
		}
		p := filepath.Clean(filepath.Join(root, "final.json"))
		if seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	add(e.LogsRoot)
	add(e.baseLogsRoot)
	return out
}

func effectiveStageTimeout(node *model.Node, global time.Duration) time.Duration {
	nodeTimeout := time.Duration(0)
	// parseDuration accepts explicit durations and bare second counts.
	if node != nil {
		nodeTimeout = parseDuration(node.Attr("timeout", ""), 0)
	}
	return minPositiveDuration(nodeTimeout, global)
}

func minPositiveDuration(a, b time.Duration) time.Duration {
	switch {
	case a > 0 && b > 0:
		if a < b {
			return a
		}
		return b
	case a > 0:
		return a
	case b > 0:
		return b
	default:
		return 0
	}
}

func (e *Engine) runStallWatchdog(ctx context.Context, cancel context.CancelCauseFunc, stallTimeout time.Duration, checkEvery time.Duration) {
	if e == nil || cancel == nil || stallTimeout <= 0 {
		return
	}
	if checkEvery <= 0 {
		checkEvery = 5 * time.Second
	}
	ticker := time.NewTicker(checkEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			last := e.lastProgressTime()
			if last.IsZero() {
				last = time.Now().UTC()
				e.setLastProgressTime(last)
			}
			idle := time.Since(last)
			if idle < stallTimeout {
				continue
			}
			e.appendProgress(map[string]any{
				"event":            "stall_watchdog_timeout",
				"stall_timeout_ms": stallTimeout.Milliseconds(),
				"idle_ms":          idle.Milliseconds(),
			})
			cancel(fmt.Errorf("stall watchdog timeout after %s with no progress", stallTimeout))
			return
		}
	}
}

func writeJSON(path string, v any) error {
	return runtime.WriteJSONAtomicFile(path, v)
}

func copyStringIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func copyStringStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func parseInt(s string, def int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err != nil {
		return def
	}
	return n
}

func defaultLogsRoot(runID string) string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home := os.Getenv("HOME")
		if home == "" {
			base = "."
		} else {
			base = filepath.Join(home, ".local", "state")
		}
	}
	return filepath.Join(base, "kilroy", "attractor", "runs", runID)
}

func expandGoal(g *model.Graph) {
	goal := g.Attrs["goal"]
	if goal == "" {
		return
	}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if p := n.Attrs["prompt"]; strings.Contains(p, "$goal") {
			n.Attrs["prompt"] = strings.ReplaceAll(p, "$goal", goal)
		}
		if p := n.Attrs["llm_prompt"]; strings.Contains(p, "$goal") {
			n.Attrs["llm_prompt"] = strings.ReplaceAll(p, "$goal", goal)
		}
	}
}

// expandBaseSHA replaces $base_sha placeholders in all node prompts. Called after
// the run's base SHA is known (later than $goal expansion which happens at parse time).
func expandBaseSHA(g *model.Graph, baseSHA string) {
	if baseSHA == "" {
		return
	}
	for _, n := range g.Nodes {
		if n == nil {
			continue
		}
		if p := n.Attrs["prompt"]; strings.Contains(p, "$base_sha") {
			n.Attrs["prompt"] = strings.ReplaceAll(p, "$base_sha", baseSHA)
		}
		if p := n.Attrs["llm_prompt"]; strings.Contains(p, "$base_sha") {
			n.Attrs["llm_prompt"] = strings.ReplaceAll(p, "$base_sha", baseSHA)
		}
	}
}

func isTerminal(n *model.Node) bool {
	return n != nil && (n.Shape() == "Msquare" || n.Shape() == "doublecircle" || strings.EqualFold(n.ID, "exit") || strings.EqualFold(n.ID, "end"))
}

func checkGoalGates(g *model.Graph, outcomes map[string]runtime.Outcome) (bool, string) {
	for id, out := range outcomes {
		n := g.Nodes[id]
		if n == nil {
			continue
		}
		if !strings.EqualFold(n.Attr("goal_gate", "false"), "true") {
			continue
		}
		if out.Status != runtime.StatusSuccess && out.Status != runtime.StatusPartialSuccess {
			return false, id
		}
	}
	return true, ""
}

func findStartNodeID(g *model.Graph) string {
	for id, n := range g.Nodes {
		if n != nil && (n.Shape() == "Mdiamond" || n.Shape() == "circle") {
			return id
		}
	}
	for id := range g.Nodes {
		if strings.EqualFold(id, "start") {
			return id
		}
	}
	return ""
}

// selectNextEdge implements attractor-spec edge selection with deterministic tie-breaks (metaspec).
func selectNextEdge(g *model.Graph, from string, out runtime.Outcome, ctx *runtime.Context) (*model.Edge, error) {
	edges, err := selectAllEligibleEdges(g, from, out, ctx)
	if err != nil {
		return nil, err
	}
	if len(edges) == 0 {
		return nil, nil
	}
	return bestEdge(edges), nil
}

// hasMatchingOutgoingCondition returns true if any outgoing edge from the given
// node has a condition that matches the outcome. Used by executeWithRetry to
// recognize custom outcomes (e.g. "needs_dod", "process") as routing decisions
// rather than failures requiring retry. A snapshot of the live context is used
// with the outcome's context_updates applied, mirroring what the main loop does
// before edge selection (engine.go §523-525).
func hasMatchingOutgoingCondition(g *model.Graph, nodeID string, out runtime.Outcome, ctx *runtime.Context) bool {
	// Clone the context and apply the outcome's updates so that conditions
	// referencing context.* keys set by this node evaluate correctly.
	evalCtx := ctx.Clone()
	evalCtx.ApplyUpdates(out.ContextUpdates)
	evalCtx.Set("outcome", string(out.Status))
	for _, e := range g.Outgoing(nodeID) {
		if e == nil {
			continue
		}
		c := strings.TrimSpace(e.Condition())
		if c == "" {
			continue
		}
		ok, err := cond.Evaluate(c, out, evalCtx)
		if err == nil && ok {
			return true
		}
	}
	return false
}

// selectAllEligibleEdges returns all edges that are eligible for traversal from the given node.
// When multiple edges are returned, the caller should treat this as an implicit fan-out.
// Preferred-label and suggested-next-ID narrowing still apply — if they narrow to a single edge,
// only that edge is returned (no fan-out).
func selectAllEligibleEdges(g *model.Graph, from string, out runtime.Outcome, ctx *runtime.Context) ([]*model.Edge, error) {
	rawEdges := g.Outgoing(from)
	if len(rawEdges) == 0 {
		return nil, nil
	}

	// Filter nil edges once for use in all subsequent steps.
	var edges []*model.Edge
	for _, e := range rawEdges {
		if e != nil {
			edges = append(edges, e)
		}
	}
	if len(edges) == 0 {
		return nil, nil
	}

	// Step 1: Eligible conditional edges.
	var condMatched []*model.Edge
	for _, e := range edges {
		c := strings.TrimSpace(e.Condition())
		if c == "" {
			continue
		}
		ok, err := cond.Evaluate(c, out, ctx)
		if err != nil {
			return nil, err
		}
		if ok {
			condMatched = append(condMatched, e)
		}
	}
	if len(condMatched) > 0 {
		return condMatched, nil
	}

	// Step 2: Preferred label match narrows to one.
	// Per spec §3.3, iterates ALL edges (not just unconditional).
	if strings.TrimSpace(out.PreferredLabel) != "" {
		want := normalizeLabel(out.PreferredLabel)
		sorted := make([]*model.Edge, len(edges))
		copy(sorted, edges)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
		for _, e := range sorted {
			if normalizeLabel(e.Label()) == want {
				return []*model.Edge{e}, nil
			}
		}
	}

	// Step 3: Suggested next IDs narrow to one.
	// Per spec §3.3, iterates ALL edges (not just unconditional).
	if len(out.SuggestedNextIDs) > 0 {
		sorted := make([]*model.Edge, len(edges))
		copy(sorted, edges)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Order < sorted[j].Order })
		for _, suggested := range out.SuggestedNextIDs {
			for _, e := range sorted {
				if e.To == suggested {
					return []*model.Edge{e}, nil
				}
			}
		}
	}

	// Steps 4 & 5: Weight with lexical tiebreak (unconditional edges only).
	var uncond []*model.Edge
	for _, e := range edges {
		if strings.TrimSpace(e.Condition()) == "" {
			uncond = append(uncond, e)
		}
	}
	if len(uncond) > 0 {
		return uncond, nil
	}

	// No eligible edges: all edges have conditions and none matched, and no
	// unconditional fallback edge exists. Return nil — the caller will treat
	// this as a routing gap (the graph needs an unconditional edge or more
	// complete condition coverage). This is safer than silently routing through
	// condition-failed edges, which can cause surprising behavior (e.g., a
	// failed node's output routed through a "success-only" edge).
	return nil, nil
}

func bestEdge(edges []*model.Edge) *model.Edge {
	// metaspec: weight desc, to_node asc, then edge declaration order asc.
	sort.SliceStable(edges, func(i, j int) bool {
		wi := parseInt(edges[i].Attr("weight", "0"), 0)
		wj := parseInt(edges[j].Attr("weight", "0"), 0)
		if wi != wj {
			return wi > wj
		}
		if edges[i].To != edges[j].To {
			return edges[i].To < edges[j].To
		}
		return edges[i].Order < edges[j].Order
	})
	return edges[0]
}

func normalizeLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	// Strip common accelerator prefixes: "[K] ", "K) ", "K - "
	if len(s) >= 4 && s[0] == '[' && s[2] == ']' && s[3] == ' ' {
		return strings.TrimSpace(s[4:])
	}
	if len(s) >= 3 && s[1] == ')' && s[2] == ' ' {
		return strings.TrimSpace(s[3:])
	}
	if len(s) >= 4 && s[1] == ' ' && s[2] == '-' && s[3] == ' ' {
		return strings.TrimSpace(s[4:])
	}
	return s
}
