// RunDB lifecycle hooks. Called at run/node/edge lifecycle points.
// All operations are best-effort: errors produce warnings, never block execution.
package engine

import (
	"strings"

	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

func (e *Engine) rundbRecordRunStart() {
	if e == nil || e.RunDB == nil {
		return
	}
	goal := ""
	if e.Graph != nil {
		goal = e.Graph.Attrs["goal"]
	}
	graphName := ""
	if e.Graph != nil {
		graphName = e.Graph.Name
	}
	if err := e.RunDB.RecordRunStart(
		e.Options.RunID, graphName, goal, "running",
		e.LogsRoot, e.WorktreeDir, e.RunBranch, e.Options.RepoPath,
		string(e.DotSource), e.Options.Inputs, e.Options.Labels,
	); err != nil {
		e.Warn("rundb: record run start: " + err.Error())
	}
}

func (e *Engine) rundbRecordRunComplete(status runtime.FinalStatus, failureReason, finalSHA string) {
	if e == nil || e.RunDB == nil {
		return
	}
	if err := e.RunDB.RecordRunComplete(
		e.Options.RunID, string(status), failureReason, finalSHA, e.warningsCopy(),
	); err != nil {
		e.Warn("rundb: record run complete: " + err.Error())
	}
}

func (e *Engine) rundbRecordNodeStart(nodeID string, attempt int, handlerType string) int64 {
	if e == nil || e.RunDB == nil {
		return 0
	}
	id, err := e.RunDB.RecordNodeStart(e.Options.RunID, nodeID, attempt, handlerType)
	if err != nil {
		e.Warn("rundb: record node start: " + err.Error())
		return 0
	}
	return id
}

func (e *Engine) rundbRecordNodeComplete(dbID int64, out runtime.Outcome) {
	if e == nil || e.RunDB == nil || dbID == 0 {
		return
	}
	failureClass := ""
	if meta, ok := out.Meta["failure_class"]; ok {
		if s, ok := meta.(string); ok {
			failureClass = s
		}
	}
	if err := e.RunDB.RecordNodeComplete(
		dbID, string(out.Status), out.FailureReason, failureClass,
		out.PreferredLabel, out.Notes, out.ContextUpdates,
	); err != nil {
		e.Warn("rundb: record node complete: " + err.Error())
	}
}

func (e *Engine) rundbRecordEdgeDecision(fromNode, toNode, edgeLabel, reason string) {
	if e == nil || e.RunDB == nil {
		return
	}
	if err := e.RunDB.RecordEdgeDecision(
		e.Options.RunID, fromNode, toNode, edgeLabel, reason,
	); err != nil {
		e.Warn("rundb: record edge decision: " + err.Error())
	}
}

func (e *Engine) rundbRecordProviderIfAgent(nodeID string, attempt int) {
	if e == nil || e.RunDB == nil || e.Graph == nil {
		return
	}
	node := e.Graph.Nodes[nodeID]
	if node == nil {
		return
	}
	provider := node.Attrs["llm_provider"]
	model := node.Attrs["llm_model"]
	if provider == "" && model == "" {
		return
	}
	backend := node.Attrs["backend"]
	if backend == "" {
		backend = "cli"
	}
	if err := e.RunDB.RecordProviderSelection(
		e.Options.RunID, nodeID, attempt, provider, model, backend,
	); err != nil {
		e.Warn("rundb: record provider selection: " + err.Error())
	}
}

// resolvedHandlerTypeName returns the handler type string for a node.
func resolvedHandlerTypeName(e *Engine, nodeID string) string {
	if e == nil || e.Graph == nil || e.Registry == nil {
		return ""
	}
	node := e.Graph.Nodes[nodeID]
	if node == nil {
		return ""
	}
	if t := strings.TrimSpace(node.TypeOverride()); t != "" {
		return t
	}
	return shapeToType(node.Shape())
}
