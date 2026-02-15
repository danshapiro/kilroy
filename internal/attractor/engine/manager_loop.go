package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/cond"
	"github.com/danshapiro/kilroy/internal/attractor/model"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// childResult holds the outcome of a child pipeline execution.
type childResult struct {
	Outcome runtime.Outcome
	Error   error
}

// Execute implements the ManagerLoopHandler per spec §4.11.
// It runs an observe/wait loop that monitors a child pipeline and evaluates
// stop conditions each cycle. The steer action is logged but not yet
// implemented (deferred to v2).
func (h *ManagerLoopHandler) Execute(ctx context.Context, exec *Execution, node *model.Node) (runtime.Outcome, error) {
	if exec == nil || exec.Engine == nil || exec.Graph == nil {
		return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "manager loop missing execution context"}, nil
	}

	// Parse node attributes per spec §4.11.
	pollInterval := parseDuration(node.Attr("manager.poll_interval", "45s"), 45*time.Second)
	maxCycles := parseInt(node.Attr("manager.max_cycles", "1000"), 1000)
	if maxCycles <= 0 {
		maxCycles = 1000
	}
	stopCondition := strings.TrimSpace(node.Attr("manager.stop_condition", ""))
	actionsRaw := strings.TrimSpace(node.Attr("manager.actions", "observe,wait"))
	actions := parseManagerActions(actionsRaw)

	// Resolve child dotfile from graph attrs then node attrs.
	childDotfile := strings.TrimSpace(exec.Graph.Attrs["stack.child_dotfile"])
	if childDotfile == "" {
		childDotfile = strings.TrimSpace(node.Attr("stack.child_dotfile", ""))
	}

	// Auto-start child if configured.
	autostart := strings.EqualFold(strings.TrimSpace(node.Attr("stack.child_autostart", "true")), "true")

	// Fail fast: child_autostart defaults to true, so if no child_dotfile is
	// configured the loop would silently run the full observation cycle
	// (default: 1000 cycles * 45s = ~12.5 hours) before reporting max-cycles
	// exceeded. Catch this misconfiguration early with an actionable error.
	if autostart && childDotfile == "" {
		return runtime.Outcome{
			Status:        runtime.StatusFail,
			FailureReason: "stack.child_autostart is true but stack.child_dotfile is not configured",
		}, nil
	}

	var childCancel context.CancelFunc
	childDone := make(chan childResult, 1)

	if autostart && childDotfile != "" {
		var childCtx context.Context
		childCtx, childCancel = context.WithCancel(ctx)
		go func() {
			result := runChildPipeline(childCtx, exec, childDotfile, node.ID)
			childDone <- result
		}()
	}

	defer func() {
		if childCancel != nil {
			childCancel()
		}
	}()

	// Log steer action as deferred to v2.
	if actions["steer"] {
		exec.Engine.Warn(fmt.Sprintf("manager_loop node %s: 'steer' action is not yet implemented (v2)", node.ID))
	}

	// Observation loop per spec §4.11 pseudocode.
	for cycle := 1; cycle <= maxCycles; cycle++ {
		if err := ctx.Err(); err != nil {
			return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "manager loop canceled"}, nil
		}

		exec.Engine.appendProgress(map[string]any{
			"event":      "manager_loop_cycle",
			"node_id":    node.ID,
			"cycle":      cycle,
			"max_cycles": maxCycles,
		})

		// Observe: check if child pipeline has completed.
		if actions["observe"] {
			select {
			case result := <-childDone:
				childDone = nil // prevent re-reading from closed channel semantics
				if result.Outcome.Status == runtime.StatusSuccess || result.Outcome.Status == runtime.StatusPartialSuccess {
					return runtime.Outcome{
						Status: runtime.StatusSuccess,
						Notes:  fmt.Sprintf("child pipeline completed successfully at cycle %d", cycle),
						ContextUpdates: map[string]any{
							"stack.child.status":  "completed",
							"stack.child.outcome": string(result.Outcome.Status),
						},
					}, nil
				}
				return runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: fmt.Sprintf("child pipeline failed: %s", result.Outcome.FailureReason),
					ContextUpdates: map[string]any{
						"stack.child.status":  "failed",
						"stack.child.outcome": string(result.Outcome.Status),
					},
				}, nil
			default:
				// Child still running, continue loop.
			}
		}

		// Evaluate stop condition (spec §4.11 pseudocode line: IF stop_condition is not empty).
		if stopCondition != "" {
			ok, err := cond.Evaluate(stopCondition, runtime.Outcome{Status: runtime.StatusSuccess}, exec.Context)
			if err != nil {
				// Invalid/malformed stop condition — fail immediately with an
				// actionable error instead of silently looping until max_cycles.
				exec.Engine.Warn(fmt.Sprintf("manager_loop node %s: stop_condition evaluation error: %v", node.ID, err))
				if childCancel != nil {
					childCancel()
				}
				return runtime.Outcome{
					Status:        runtime.StatusFail,
					FailureReason: fmt.Sprintf("invalid stop_condition %q: %v", stopCondition, err),
				}, nil
			}
			if ok {
				if childCancel != nil {
					childCancel()
				}
				return runtime.Outcome{
					Status: runtime.StatusSuccess,
					Notes:  fmt.Sprintf("stop condition satisfied at cycle %d", cycle),
				}, nil
			}
		}

		// Wait before next cycle.
		if actions["wait"] && cycle < maxCycles {
			if !sleepWithContext(ctx, pollInterval) {
				return runtime.Outcome{Status: runtime.StatusFail, FailureReason: "manager loop canceled during wait"}, nil
			}
		}
	}

	// Max cycles exceeded.
	if childCancel != nil {
		childCancel()
	}
	return runtime.Outcome{
		Status:        runtime.StatusFail,
		FailureReason: fmt.Sprintf("manager loop max cycles exceeded (%d)", maxCycles),
	}, nil
}

// parseManagerActions splits a comma-separated actions string into a lookup map.
func parseManagerActions(s string) map[string]bool {
	actions := map[string]bool{}
	for _, a := range strings.Split(s, ",") {
		a = strings.ToLower(strings.TrimSpace(a))
		if a != "" {
			actions[a] = true
		}
	}
	return actions
}

// runChildPipeline loads and executes a child DOT pipeline, returning the result.
// This reuses the sub-pipeline execution infrastructure (Prepare, runSubgraphUntil)
// already built for parallel branches.
func runChildPipeline(ctx context.Context, exec *Execution, childDotfile string, managerNodeID string) childResult {
	// Resolve child dotfile path relative to the active run worktree (not the
	// source repo). Earlier stages may generate or modify child dotfiles in the
	// worktree, so reading from Options.RepoPath would see stale/missing content.
	dotPath := childDotfile
	if !filepath.IsAbs(dotPath) && exec.WorktreeDir != "" {
		dotPath = filepath.Join(exec.WorktreeDir, dotPath)
	} else if !filepath.IsAbs(dotPath) && exec.Engine.Options.RepoPath != "" {
		// Fallback to repo path only if worktree dir is not available.
		dotPath = filepath.Join(exec.Engine.Options.RepoPath, dotPath)
	}

	dotSource, err := os.ReadFile(dotPath)
	if err != nil {
		return childResult{
			Outcome: runtime.Outcome{Status: runtime.StatusFail, FailureReason: fmt.Sprintf("read child dotfile: %v", err)},
			Error:   err,
		}
	}

	// Use PrepareWithOptions so prompt_file attributes in the child graph resolve
	// relative to the worktree (the active execution context), not the source repo.
	repoPath := exec.WorktreeDir
	if repoPath == "" {
		repoPath = exec.Engine.Options.RepoPath
	}
	childGraph, _, err := PrepareWithOptions(dotSource, PrepareOptions{
		RepoPath: repoPath,
	})
	if err != nil {
		return childResult{
			Outcome: runtime.Outcome{Status: runtime.StatusFail, FailureReason: fmt.Sprintf("prepare child graph: %v", err)},
			Error:   err,
		}
	}

	// Find the start node in the child graph.
	startID := findStartNodeID(childGraph)
	if startID == "" {
		return childResult{
			Outcome: runtime.Outcome{Status: runtime.StatusFail, FailureReason: "child graph has no start node"},
		}
	}

	// Find an exit (terminal) node in the child graph.
	exitID := findExitNodeID(childGraph)

	// Create a child engine using the same infrastructure as parallel branches.
	childLogsRoot := filepath.Join(exec.LogsRoot, managerNodeID, "child")
	_ = os.MkdirAll(childLogsRoot, 0o755)

	childEng := &Engine{
		Graph:              childGraph,
		Options:            exec.Engine.Options,
		DotSource:          dotSource,
		WorktreeDir:        exec.WorktreeDir,
		LogsRoot:           childLogsRoot,
		Context:            exec.Context.Clone(),
		Registry:           exec.Engine.Registry,
		CodergenBackend:    exec.Engine.CodergenBackend,
		Interviewer:        exec.Engine.Interviewer,
		ModelCatalogSHA:    exec.Engine.ModelCatalogSHA,
		ModelCatalogSource: exec.Engine.ModelCatalogSource,
		ModelCatalogPath:   exec.Engine.ModelCatalogPath,
	}

	res, err := runSubgraphUntil(ctx, childEng, startID, exitID)
	if err != nil {
		return childResult{
			Outcome: runtime.Outcome{Status: runtime.StatusFail, FailureReason: err.Error()},
			Error:   err,
		}
	}

	return childResult{
		Outcome: res.Outcome,
	}
}

// findExitNodeID locates a terminal node in the graph (for use as a stop boundary).
// Returns empty string if no terminal node is found, which means the subgraph
// will run until it runs out of edges.
func findExitNodeID(g *model.Graph) string {
	if g == nil {
		return ""
	}
	for id, n := range g.Nodes {
		if isTerminal(n) {
			return id
		}
	}
	return ""
}
