// Write operations for the run database.
// Called by the engine at lifecycle points: run start, node start, node complete, edge selection, run complete.
package rundb

import (
	"encoding/json"
	"time"
)

// RunRecord represents a run row for insertion or update.
type RunRecord struct {
	RunID       string
	GraphName   string
	Goal        string
	Status      string
	LogsRoot    string
	WorktreeDir string
	RunBranch   string
	RepoPath    string
	StartedAt   time.Time
	DotSource   string
	Inputs      map[string]any
	Labels      map[string]string
	Invocation  []string
	Config      map[string]any
}

// RecordRunStart satisfies engine.RunDBWriter. Delegates to InsertRun.
func (d *DB) RecordRunStart(runID, graphName, goal, status, logsRoot, worktreeDir, runBranch, repoPath, dotSource string, inputs map[string]any, labels map[string]string, invocation []string, config map[string]any) error {
	return d.InsertRun(RunRecord{
		RunID: runID, GraphName: graphName, Goal: goal, Status: status,
		LogsRoot: logsRoot, WorktreeDir: worktreeDir, RunBranch: runBranch,
		RepoPath: repoPath, DotSource: dotSource, Inputs: inputs, Labels: labels,
		Invocation: invocation, Config: config,
		StartedAt: time.Now(),
	})
}

// RecordRunComplete satisfies engine.RunDBWriter. Delegates to CompleteRun.
func (d *DB) RecordRunComplete(runID, status, failureReason, finalSHA string, warnings []string) error {
	return d.CompleteRun(runID, status, failureReason, finalSHA, warnings)
}

// RecordNodeStart satisfies engine.RunDBWriter. Delegates to InsertNodeStart.
func (d *DB) RecordNodeStart(runID, nodeID string, attempt int, handlerType string) (int64, error) {
	return d.InsertNodeStart(runID, nodeID, attempt, handlerType)
}

// RecordNodeComplete satisfies engine.RunDBWriter. Delegates to CompleteNode.
func (d *DB) RecordNodeComplete(id int64, status, failureReason, failureClass, preferredLabel, notes string, contextUpdates map[string]any) error {
	return d.CompleteNode(id, status, failureReason, failureClass, preferredLabel, notes, contextUpdates)
}

// RecordEdgeDecision satisfies engine.RunDBWriter. Delegates to InsertEdgeDecision.
func (d *DB) RecordEdgeDecision(runID, fromNode, toNode, edgeLabel, condition, reason string) error {
	return d.InsertEdgeDecision(runID, fromNode, toNode, edgeLabel, condition, reason)
}

// RecordProviderSelection satisfies engine.RunDBWriter. Delegates to InsertProviderSelection.
func (d *DB) RecordProviderSelection(runID, nodeID string, attempt int, provider, model, backend string) error {
	return d.InsertProviderSelection(runID, nodeID, attempt, provider, model, backend)
}

// InsertRun records a new run at start time.
func (d *DB) InsertRun(r RunRecord) error {
	inputsJSON, _ := json.Marshal(r.Inputs)
	labelsJSON, _ := json.Marshal(r.Labels)
	invocationJSON, _ := json.Marshal(r.Invocation)
	configJSON, _ := json.Marshal(r.Config)
	startedAt := r.StartedAt.UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`INSERT OR REPLACE INTO runs
		(run_id, graph_name, goal, status, logs_root, worktree_dir, run_branch, repo_path, started_at, dot_source, inputs_json, labels_json, invocation_json, config_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.RunID, r.GraphName, r.Goal, r.Status, r.LogsRoot, r.WorktreeDir,
		r.RunBranch, r.RepoPath, startedAt, r.DotSource, string(inputsJSON), string(labelsJSON),
		string(invocationJSON), string(configJSON))
	return err
}

// CompleteRun updates a run with final status and timing.
func (d *DB) CompleteRun(runID, status, failureReason, finalSHA string, warnings []string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	warningsJSON, _ := json.Marshal(warnings)
	_, err := d.db.Exec(`UPDATE runs SET
		status = ?, completed_at = ?, failure_reason = ?, final_sha = ?, warnings_json = ?,
		duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
		WHERE run_id = ?`,
		status, now, failureReason, finalSHA, string(warningsJSON), now, runID)
	return err
}

// NodeExecution represents a node execution record.
type NodeExecution struct {
	RunID          string
	NodeID         string
	Attempt        int
	HandlerType    string
	Status         string
	StartedAt      time.Time
	CompletedAt    *time.Time
	DurationMS     *int64
	FailureReason  string
	FailureClass   string
	PreferredLabel string
	ContextUpdates map[string]any
	Notes          string
}

// InsertNodeStart records the start of a node execution.
func (d *DB) InsertNodeStart(runID, nodeID string, attempt int, handlerType string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := d.db.Exec(`INSERT INTO node_executions
		(run_id, node_id, attempt, handler_type, started_at)
		VALUES (?, ?, ?, ?, ?)`,
		runID, nodeID, attempt, handlerType, now)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// CompleteNode updates a node execution with outcome data.
func (d *DB) CompleteNode(id int64, status, failureReason, failureClass, preferredLabel, notes string, contextUpdates map[string]any) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	updatesJSON, _ := json.Marshal(contextUpdates)
	_, err := d.db.Exec(`UPDATE node_executions SET
		status = ?, completed_at = ?, failure_reason = ?, failure_class = ?,
		preferred_label = ?, context_updates_json = ?, notes = ?,
		duration_ms = CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
		WHERE id = ?`,
		status, now, failureReason, failureClass, preferredLabel, string(updatesJSON), notes, now, id)
	return err
}

// InsertEdgeDecision records an edge selection decision.
func (d *DB) InsertEdgeDecision(runID, fromNode, toNode, edgeLabel, condition, reason string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`INSERT INTO edge_decisions
		(run_id, from_node, to_node, edge_label, condition, reason, decided_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, fromNode, toNode, edgeLabel, condition, reason, now)
	return err
}

// InsertProviderSelection records a provider/model selection for a node.
func (d *DB) InsertProviderSelection(runID, nodeID string, attempt int, provider, model, backend string) error {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := d.db.Exec(`INSERT INTO provider_selections
		(run_id, node_id, attempt, provider, model, backend, selected_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		runID, nodeID, attempt, provider, model, backend, now)
	return err
}

// RecordNodeDiff records the git diff for a node execution.
func (d *DB) RecordNodeDiff(runID, nodeID string, attempt int, beforeSHA, afterSHA string, filesChanged, insertions, deletions int) error {
	_, err := d.db.Exec(`INSERT INTO node_diffs
		(run_id, node_id, attempt, before_sha, after_sha, files_changed, insertions, deletions)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		runID, nodeID, attempt, beforeSHA, afterSHA, filesChanged, insertions, deletions)
	return err
}
