// RunDBWriter defines the interface for writing run state to the database.
// Implemented by rundb.DB; defined here so engine/ doesn't import rundb/.
package engine

// RunDBWriter is the interface the engine uses to record run state.
// All methods are best-effort: errors are logged as warnings, never fatal.
type RunDBWriter interface {
	RecordRunStart(runID, graphName, goal, status, logsRoot, worktreeDir, runBranch, repoPath, dotSource string, inputs map[string]any, labels map[string]string) error
	RecordRunComplete(runID, status, failureReason, finalSHA string, warnings []string) error
	RecordNodeStart(runID, nodeID string, attempt int, handlerType string) (int64, error)
	RecordNodeComplete(id int64, status, failureReason, failureClass, preferredLabel, notes string, contextUpdates map[string]any) error
	RecordEdgeDecision(runID, fromNode, toNode, edgeLabel, reason string) error
	RecordProviderSelection(runID, nodeID string, attempt int, provider, model, backend string) error
}
