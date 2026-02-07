package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	rdebug "runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/strongdm/kilroy/internal/attractor/cond"
	"github.com/strongdm/kilroy/internal/attractor/dot"
	"github.com/strongdm/kilroy/internal/attractor/gitutil"
	"github.com/strongdm/kilroy/internal/attractor/model"
	"github.com/strongdm/kilroy/internal/attractor/runtime"
	"github.com/strongdm/kilroy/internal/attractor/style"
	"github.com/strongdm/kilroy/internal/attractor/validate"
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

	// Model catalog snapshot metadata (metaspec).
	ModelCatalogSHA    string
	ModelCatalogSource string
	ModelCatalogPath   string

	warningsMu sync.Mutex
	Warnings   []string

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
}

type PrepareOptions struct {
	Transforms []Transform
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

	// Built-in transforms: stylesheet, $goal expansion.
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

	diags := validate.Validate(g)
	for _, d := range diags {
		if d.Severity == validate.SeverityError {
			return g, diags, fmt.Errorf("validation error: %s: %s", d.Rule, d.Message)
		}
	}
	return g, diags, nil
}

// Run executes the pipeline in a dedicated git worktree and creates a checkpoint commit after each node.
func Run(ctx context.Context, dotSource []byte, opts RunOptions) (*Result, error) {
	if err := opts.applyDefaults(); err != nil {
		return nil, err
	}
	g, _, err := Prepare(dotSource)
	if err != nil {
		return nil, err
	}

	eng := &Engine{
		Graph:           g,
		Options:         opts,
		DotSource:       append([]byte{}, dotSource...),
		LogsRoot:        opts.LogsRoot,
		WorktreeDir:     opts.WorktreeDir,
		Context:         runtime.NewContext(),
		Registry:        NewDefaultRegistry(),
		Interviewer:     &AutoApproveInterviewer{},
		CodergenBackend: &SimulatedCodergenBackend{},
	}
	eng.RunBranch = fmt.Sprintf("%s/%s", opts.RunBranchPrefix, opts.RunID)

	return eng.run(ctx)
}

func (e *Engine) run(ctx context.Context) (*Result, error) {
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
	if err := os.MkdirAll(e.LogsRoot, 0o755); err != nil {
		return nil, err
	}
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
	if err := e.cxdbRunStarted(ctx, baseSHA); err != nil {
		return nil, err
	}

	// Mirror graph attributes into context.
	for k, v := range e.Graph.Attrs {
		e.Context.Set("graph."+k, v)
	}
	e.Context.Set("graph.goal", e.Graph.Attrs["goal"])

	current := findStartNodeID(e.Graph)
	if current == "" {
		return nil, fmt.Errorf("no start node found")
	}

	completed := []string{}
	nodeRetries := map[string]int{}

	// Node outcomes used for goal_gate checks.
	nodeOutcomes := map[string]runtime.Outcome{}

	return e.runLoop(ctx, current, completed, nodeRetries, nodeOutcomes)
}

func (e *Engine) runLoop(ctx context.Context, current string, completed []string, nodeRetries map[string]int, nodeOutcomes map[string]runtime.Outcome) (*Result, error) {
	for {
		node := e.Graph.Nodes[current]
		if node == nil {
			return nil, fmt.Errorf("missing node: %s", current)
		}
		prev := ""
		if len(completed) > 0 {
			prev = completed[len(completed)-1]
		}
		e.Context.Set("previous_node", prev)
		e.Context.Set("current_node", current)
		e.Context.Set("completed_nodes", append([]string{}, completed...))

		// Resolve fidelity/thread info for LLM nodes for checkpointing + resume semantics.
		if resolvedHandlerType(node) == "codergen" {
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
			sha, err := e.checkpoint(node.ID, out, completed, nodeRetries)
			if err != nil {
				return nil, err
			}
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
			finalPath := filepath.Join(e.LogsRoot, "final.json")
			_ = final.Save(finalPath)
			if e.CXDB != nil {
				_, _ = e.CXDB.PutArtifactFile(ctx, "", "final.json", finalPath)
			}
			// Convenience tarball (metaspec SHOULD): run.tgz excluding worktree/.
			runTar := filepath.Join(e.LogsRoot, "run.tgz")
			_ = writeTarGz(runTar, e.LogsRoot, func(rel string, d os.DirEntry) bool {
				if rel == "run.tgz" || rel == "run.tgz.tmp" {
					return false
				}
				if rel == "worktree" || strings.HasPrefix(rel, "worktree/") {
					return false
				}
				return true
			})
			if e.CXDB != nil {
				if _, err := os.Stat(runTar); err == nil {
					_, _ = e.CXDB.PutArtifactFile(ctx, "", "run.tgz", runTar)
				}
			}
			return &Result{
				RunID:          e.Options.RunID,
				LogsRoot:       e.LogsRoot,
				WorktreeDir:    e.WorktreeDir,
				RunBranch:      e.RunBranch,
				FinalStatus:    runtime.FinalSuccess,
				FinalCommitSHA: sha,
			}, nil
		}

		e.cxdbStageStarted(ctx, node)
		out, err := e.executeWithRetry(ctx, node, nodeRetries)
		if err != nil {
			return nil, err
		}
		e.cxdbStageFinished(ctx, node, out)

		// Record completion.
		completed = append(completed, node.ID)
		nodeOutcomes[node.ID] = out

		// Apply context updates and built-ins.
		e.Context.ApplyUpdates(out.ContextUpdates)
		e.Context.Set("outcome", string(out.Status))
		e.Context.Set("preferred_label", out.PreferredLabel)

		// Checkpoint (git commit + checkpoint.json).
		sha, err := e.checkpoint(node.ID, out, completed, nodeRetries)
		if err != nil {
			return nil, err
		}
		e.cxdbCheckpointSaved(ctx, node.ID, out.Status, sha)

		// Kilroy v1: parallel nodes control the next hop (join node) via context.
		// This keeps the DOT surface simple while allowing deterministic fan-out/fan-in.
		if t := strings.TrimSpace(node.TypeOverride()); t == "parallel" || (t == "" && shapeToType(node.Shape()) == "parallel") {
			join := strings.TrimSpace(e.Context.GetString("parallel.join_node", ""))
			if join == "" {
				return nil, fmt.Errorf("parallel node missing parallel.join_node in context")
			}
			e.incomingEdge = nil
			current = join
			continue
		}

		// Select next edge.
		next, err := selectNextEdge(e.Graph, node.ID, out, e.Context)
		if err != nil {
			return nil, err
		}
		if next == nil {
			if out.Status == runtime.StatusFail {
				failedTurnID, _ := e.cxdbRunFailed(ctx, node.ID, sha, out.FailureReason)
				final := runtime.FinalOutcome{
					Timestamp:         time.Now().UTC(),
					Status:            runtime.FinalFail,
					RunID:             e.Options.RunID,
					FinalGitCommitSHA: sha,
					CXDBContextID:     cxdbContextID(e.CXDB),
					CXDBHeadTurnID:    failedTurnID,
				}
				finalPath := filepath.Join(e.LogsRoot, "final.json")
				_ = final.Save(finalPath)
				if e.CXDB != nil {
					_, _ = e.CXDB.PutArtifactFile(ctx, "", "final.json", finalPath)
				}
				runTar := filepath.Join(e.LogsRoot, "run.tgz")
				_ = writeTarGz(runTar, e.LogsRoot, func(rel string, d os.DirEntry) bool {
					if rel == "run.tgz" || rel == "run.tgz.tmp" {
						return false
					}
					if rel == "worktree" || strings.HasPrefix(rel, "worktree/") {
						return false
					}
					return true
				})
				if e.CXDB != nil {
					if _, err := os.Stat(runTar); err == nil {
						_, _ = e.CXDB.PutArtifactFile(ctx, "", "run.tgz", runTar)
					}
				}
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
			finalPath := filepath.Join(e.LogsRoot, "final.json")
			_ = final.Save(finalPath)
			if e.CXDB != nil {
				_, _ = e.CXDB.PutArtifactFile(ctx, "", "final.json", finalPath)
			}
			runTar := filepath.Join(e.LogsRoot, "run.tgz")
			_ = writeTarGz(runTar, e.LogsRoot, func(rel string, d os.DirEntry) bool {
				if rel == "run.tgz" || rel == "run.tgz.tmp" {
					return false
				}
				if rel == "worktree" || strings.HasPrefix(rel, "worktree/") {
					return false
				}
				return true
			})
			if e.CXDB != nil {
				if _, err := os.Stat(runTar); err == nil {
					_, _ = e.CXDB.PutArtifactFile(ctx, "", "run.tgz", runTar)
				}
			}
			return &Result{
				RunID:          e.Options.RunID,
				LogsRoot:       e.LogsRoot,
				WorktreeDir:    e.WorktreeDir,
				RunBranch:      e.RunBranch,
				FinalStatus:    runtime.FinalSuccess,
				FinalCommitSHA: sha,
			}, nil
		}

		// loop_restart is not supported in v1 beyond basic detection (metaspec prohibits auto restart semantics).
		if strings.EqualFold(next.Attr("loop_restart", "false"), "true") {
			return nil, fmt.Errorf("loop_restart not supported in v1")
		}
		e.incomingEdge = next
		current = next.To
	}
}

func (e *Engine) executeNode(ctx context.Context, node *model.Node) (runtime.Outcome, error) {
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
		}, node)
	}()
	if err != nil {
		out = runtime.Outcome{Status: runtime.StatusRetry, FailureReason: err.Error()}
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
	// Ensure required fields are present.
	if out.ContextUpdates == nil {
		out.ContextUpdates = map[string]any{}
	}
	if out.SuggestedNextIDs == nil {
		out.SuggestedNextIDs = []string{}
	}

	// Write status.json (canonical metaspec shape).
	_ = writeJSON(filepath.Join(stageDir, "status.json"), out)
	return out, nil
}

func (e *Engine) executeWithRetry(ctx context.Context, node *model.Node, retries map[string]int) (runtime.Outcome, error) {
	maxRetries := parseInt(node.Attr("max_retries", ""), 0)
	if maxRetries == 0 {
		maxRetries = parseInt(e.Graph.Attrs["default_max_retry"], 0)
	}
	if maxRetries < 0 {
		maxRetries = 0
	}
	maxAttempts := maxRetries + 1

	allowPartial := strings.EqualFold(node.Attr("allow_partial", "false"), "true")
	stageDir := filepath.Join(e.LogsRoot, node.ID)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		out, _ := e.executeNode(ctx, node)
		if out.Status == runtime.StatusSuccess || out.Status == runtime.StatusPartialSuccess || out.Status == runtime.StatusSkipped {
			retries[node.ID] = 0
			return out, nil
		}
		// FAIL and RETRY both participate in retry policy (spec DoD expects this).
		if attempt < maxAttempts {
			retries[node.ID]++
			delay := backoffDelayForNode(e.Options.RunID, e.Graph, node, attempt)
			time.Sleep(delay)
			continue
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
		sha, err = gitutil.CommitAllowEmpty(e.WorktreeDir, msg)
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
	if strings.TrimSpace(e.lastResolvedFidelity) != "" {
		if cp.Extra == nil {
			cp.Extra = map[string]any{}
		}
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
			"litellm_catalog_path":   e.ModelCatalogPath,
			"litellm_catalog_sha256": e.ModelCatalogSHA,
			"litellm_catalog_source": e.ModelCatalogSource,
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
	return writeJSON(filepath.Join(e.LogsRoot, "manifest.json"), manifest)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func copyStringIntMap(in map[string]int) map[string]int {
	out := make(map[string]int, len(in))
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
	}
}

func isTerminal(n *model.Node) bool {
	return n != nil && (n.Shape() == "Msquare" || strings.EqualFold(n.ID, "exit") || strings.EqualFold(n.ID, "end"))
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

func resolveRetryTarget(g *model.Graph, nodeID string) string {
	n := g.Nodes[nodeID]
	if n == nil {
		return ""
	}
	if t := strings.TrimSpace(n.Attr("retry_target", "")); t != "" {
		return t
	}
	if t := strings.TrimSpace(n.Attr("fallback_retry_target", "")); t != "" {
		return t
	}
	if t := strings.TrimSpace(g.Attrs["retry_target"]); t != "" {
		return t
	}
	if t := strings.TrimSpace(g.Attrs["fallback_retry_target"]); t != "" {
		return t
	}
	return ""
}

func findStartNodeID(g *model.Graph) string {
	for id, n := range g.Nodes {
		if n != nil && n.Shape() == "Mdiamond" {
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
	edges := g.Outgoing(from)
	if len(edges) == 0 {
		return nil, nil
	}

	// Eligible conditional edges.
	var condMatched []*model.Edge
	for _, e := range edges {
		if e == nil {
			continue
		}
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
		return bestEdge(condMatched), nil
	}

	// Unconditional edges are eligible when no condition matched.
	var uncond []*model.Edge
	for _, e := range edges {
		if e == nil {
			continue
		}
		if strings.TrimSpace(e.Condition()) == "" {
			uncond = append(uncond, e)
		}
	}
	if len(uncond) == 0 {
		return nil, nil
	}

	// Preferred label match (in declaration order).
	if strings.TrimSpace(out.PreferredLabel) != "" {
		want := normalizeLabel(out.PreferredLabel)
		sort.SliceStable(uncond, func(i, j int) bool { return uncond[i].Order < uncond[j].Order })
		for _, e := range uncond {
			if normalizeLabel(e.Label()) == want {
				return e, nil
			}
		}
	}

	// Suggested next IDs.
	if len(out.SuggestedNextIDs) > 0 {
		sort.SliceStable(uncond, func(i, j int) bool { return uncond[i].Order < uncond[j].Order })
		for _, suggested := range out.SuggestedNextIDs {
			for _, e := range uncond {
				if e.To == suggested {
					return e, nil
				}
			}
		}
	}

	return bestEdge(uncond), nil
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
