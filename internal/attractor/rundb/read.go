// Read operations for the run database.
// Used by CLI commands: status, runs list, runs prune.
package rundb

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
)

// RunSummary is a read-only view of a run for listing and status display.
type RunSummary struct {
	RunID         string            `json:"run_id"`
	GraphName     string            `json:"graph_name"`
	Goal          string            `json:"goal,omitempty"`
	Status        string            `json:"status"`
	LogsRoot      string            `json:"logs_root,omitempty"`
	WorktreeDir   string            `json:"worktree_dir,omitempty"`
	RunBranch     string            `json:"run_branch,omitempty"`
	RepoPath      string            `json:"repo_path,omitempty"`
	StartedAt     time.Time         `json:"started_at"`
	CompletedAt   *time.Time        `json:"completed_at,omitempty"`
	DurationMS    *int64            `json:"duration_ms,omitempty"`
	FinalSHA      string            `json:"final_sha,omitempty"`
	FailureReason string            `json:"failure_reason,omitempty"`
	Labels        map[string]string `json:"labels,omitempty"`
	Inputs        map[string]any    `json:"inputs,omitempty"`
	Warnings      []string          `json:"warnings,omitempty"`
	NodeCount     int               `json:"node_count"`
	Invocation    []string          `json:"invocation,omitempty"`
	Config        map[string]any    `json:"config,omitempty"`
}

// LatestRun returns the most recently started run.
func (d *DB) LatestRun() (*RunSummary, error) {
	runs, err := d.queryRuns("ORDER BY started_at DESC LIMIT 1", nil)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[0], nil
}

// GetRun returns a specific run by exact ID or unique prefix match.
// Returns an error if a prefix matches multiple runs.
func (d *DB) GetRun(runID string) (*RunSummary, error) {
	// Try exact match first.
	runs, err := d.queryRuns("WHERE run_id = ?", []any{runID})
	if err != nil {
		return nil, err
	}
	if len(runs) == 1 {
		return &runs[0], nil
	}
	// Try prefix match.
	runs, err = d.queryRuns("WHERE run_id LIKE ? ORDER BY started_at DESC", []any{runID + "%"})
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	if len(runs) > 1 {
		return nil, fmt.Errorf("ambiguous run ID prefix %q matches %d runs", runID, len(runs))
	}
	return &runs[0], nil
}

// ListFilter specifies filtering criteria for run listing.
type ListFilter struct {
	Status    string            // filter by status
	Labels    map[string]string // filter by label key=value
	GraphName string            // filter by graph name pattern
	Sort      string            // "newest" (default), "oldest", "longest"
	Limit     int               // max results (0 = no limit)
}

// ListRuns returns runs matching the filter, newest first.
func (d *DB) ListRuns(f ListFilter) ([]RunSummary, error) {
	var where []string
	var args []any

	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.GraphName != "" {
		where = append(where, "graph_name LIKE ?")
		args = append(args, "%"+f.GraphName+"%")
	}
	for k, v := range f.Labels {
		where = append(where, "json_extract(labels_json, ?) = ?")
		args = append(args, "$."+k, v)
	}

	clause := ""
	if len(where) > 0 {
		clause = "WHERE " + strings.Join(where, " AND ")
	}
	switch f.Sort {
	case "oldest":
		clause += " ORDER BY started_at ASC"
	case "longest":
		clause += " ORDER BY COALESCE(duration_ms, 0) DESC"
	default:
		clause += " ORDER BY started_at DESC"
	}
	if f.Limit > 0 {
		clause += fmt.Sprintf(" LIMIT %d", f.Limit)
	}
	return d.queryRuns(clause, args)
}

// PruneFilter specifies criteria for pruning old runs.
type PruneFilter struct {
	Before    *time.Time        // prune runs started before this time
	GraphName string            // prune only runs matching this graph pattern
	Labels    map[string]string // prune only runs with these labels
	Orphans   bool              // prune runs whose logs_root no longer exists
}

// PruneRuns deletes runs matching the filter and returns the count deleted.
func (d *DB) PruneRuns(f PruneFilter) (int, error) {
	if f.Orphans {
		return d.pruneOrphans()
	}

	var where []string
	var args []any

	if f.Before != nil {
		where = append(where, "started_at < ?")
		args = append(args, f.Before.UTC().Format(time.RFC3339Nano))
	}
	if f.GraphName != "" {
		where = append(where, "graph_name LIKE ?")
		args = append(args, "%"+f.GraphName+"%")
	}
	for k, v := range f.Labels {
		where = append(where, "json_extract(labels_json, ?) = ?")
		args = append(args, "$."+k, v)
	}

	if len(where) == 0 {
		return 0, fmt.Errorf("prune requires at least one filter criterion")
	}

	q := "DELETE FROM runs WHERE " + strings.Join(where, " AND ")
	result, err := d.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (d *DB) pruneOrphans() (int, error) {
	rows, err := d.db.Query("SELECT run_id, logs_root FROM runs WHERE status IN ('success', 'fail', 'canceled')")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var orphanIDs []string
	for rows.Next() {
		var runID, logsRoot string
		if err := rows.Scan(&runID, &logsRoot); err != nil {
			continue
		}
		if strings.TrimSpace(logsRoot) == "" {
			continue
		}
		if _, err := fileInfoStat(logsRoot); err != nil {
			orphanIDs = append(orphanIDs, runID)
		}
	}
	if len(orphanIDs) == 0 {
		return 0, nil
	}

	placeholders := make([]string, len(orphanIDs))
	args := make([]any, len(orphanIDs))
	for i, id := range orphanIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := "DELETE FROM runs WHERE run_id IN (" + strings.Join(placeholders, ",") + ")"
	result, err := d.db.Exec(q, args...)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

// NodeExecutionSummary is a read-only view of a node execution.
type NodeExecutionSummary struct {
	NodeID        string     `json:"node_id"`
	Attempt       int        `json:"attempt"`
	HandlerType   string     `json:"handler_type"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	CompletedAt   *time.Time `json:"completed_at,omitempty"`
	DurationMS    *int64     `json:"duration_ms,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	FailureClass  string     `json:"failure_class,omitempty"`
	Notes         string     `json:"notes,omitempty"`
}

// GetNodeExecutions returns all node executions for a run.
func (d *DB) GetNodeExecutions(runID string) ([]NodeExecutionSummary, error) {
	rows, err := d.db.Query(`SELECT node_id, attempt, handler_type, status,
		started_at, completed_at, duration_ms, failure_reason, failure_class, notes
		FROM node_executions WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []NodeExecutionSummary
	for rows.Next() {
		var n NodeExecutionSummary
		var startedAt string
		var completedAt, failureReason, failureClass, notes sql.NullString
		var durationMS sql.NullInt64
		if err := rows.Scan(&n.NodeID, &n.Attempt, &n.HandlerType, &n.Status,
			&startedAt, &completedAt, &durationMS, &failureReason, &failureClass, &notes); err != nil {
			return nil, err
		}
		n.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
			n.CompletedAt = &t
		}
		if durationMS.Valid {
			n.DurationMS = &durationMS.Int64
		}
		n.FailureReason = failureReason.String
		n.FailureClass = failureClass.String
		n.Notes = notes.String
		results = append(results, n)
	}
	return results, nil
}

// EdgeDecisionSummary is a read-only view of a routing decision.
type EdgeDecisionSummary struct {
	FromNode  string    `json:"from_node"`
	ToNode    string    `json:"to_node"`
	EdgeLabel string    `json:"edge_label,omitempty"`
	Condition string    `json:"condition,omitempty"`
	Reason    string    `json:"reason"`
	DecidedAt time.Time `json:"decided_at"`
}

// GetEdgeDecisions returns all edge decisions for a run.
func (d *DB) GetEdgeDecisions(runID string) ([]EdgeDecisionSummary, error) {
	rows, err := d.db.Query(`SELECT from_node, to_node, edge_label, COALESCE(condition, ''), reason, decided_at
		FROM edge_decisions WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []EdgeDecisionSummary
	for rows.Next() {
		var e EdgeDecisionSummary
		var decidedAt string
		if err := rows.Scan(&e.FromNode, &e.ToNode, &e.EdgeLabel, &e.Condition, &e.Reason, &decidedAt); err != nil {
			return nil, err
		}
		e.DecidedAt, _ = time.Parse(time.RFC3339Nano, decidedAt)
		results = append(results, e)
	}
	return results, nil
}

// ProviderSelectionSummary is a read-only view of a provider selection.
type ProviderSelectionSummary struct {
	NodeID   string `json:"node_id"`
	Attempt  int    `json:"attempt"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Backend  string `json:"backend"`
}

// GetProviderSelections returns all provider selections for a run.
func (d *DB) GetProviderSelections(runID string) ([]ProviderSelectionSummary, error) {
	rows, err := d.db.Query(`SELECT node_id, attempt, provider, model, backend
		FROM provider_selections WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []ProviderSelectionSummary
	for rows.Next() {
		var p ProviderSelectionSummary
		if err := rows.Scan(&p.NodeID, &p.Attempt, &p.Provider, &p.Model, &p.Backend); err != nil {
			return nil, err
		}
		results = append(results, p)
	}
	return results, nil
}

// NodeDiffSummary is a read-only view of a node's git diff.
type NodeDiffSummary struct {
	NodeID       string    `json:"node_id"`
	Attempt      int       `json:"attempt"`
	BeforeSHA    string    `json:"before_sha"`
	AfterSHA     string    `json:"after_sha"`
	FilesChanged *int      `json:"files_changed,omitempty"`
	Insertions   *int      `json:"insertions,omitempty"`
	Deletions    *int      `json:"deletions,omitempty"`
	RecordedAt   time.Time `json:"recorded_at"`
}

// GetNodeDiffs returns all node diffs for a run.
func (d *DB) GetNodeDiffs(runID string) ([]NodeDiffSummary, error) {
	rows, err := d.db.Query(`SELECT node_id, attempt, before_sha, after_sha,
		files_changed, insertions, deletions, recorded_at
		FROM node_diffs WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []NodeDiffSummary
	for rows.Next() {
		var n NodeDiffSummary
		var recordedAt string
		var filesChanged, insertions, deletions sql.NullInt64
		if err := rows.Scan(&n.NodeID, &n.Attempt, &n.BeforeSHA, &n.AfterSHA,
			&filesChanged, &insertions, &deletions, &recordedAt); err != nil {
			return nil, err
		}
		n.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
		if filesChanged.Valid {
			v := int(filesChanged.Int64)
			n.FilesChanged = &v
		}
		if insertions.Valid {
			v := int(insertions.Int64)
			n.Insertions = &v
		}
		if deletions.Valid {
			v := int(deletions.Int64)
			n.Deletions = &v
		}
		results = append(results, n)
	}
	return results, nil
}

// GetNodeDiff returns the diff for a specific node and attempt.
// If attempt is 0, returns the latest attempt.
func (d *DB) GetNodeDiff(runID, nodeID string, attempt int) (*NodeDiffSummary, error) {
	var q string
	var args []any
	if attempt > 0 {
		q = `SELECT node_id, attempt, before_sha, after_sha, files_changed, insertions, deletions, recorded_at
			FROM node_diffs WHERE run_id = ? AND node_id = ? AND attempt = ?`
		args = []any{runID, nodeID, attempt}
	} else {
		q = `SELECT node_id, attempt, before_sha, after_sha, files_changed, insertions, deletions, recorded_at
			FROM node_diffs WHERE run_id = ? AND node_id = ? ORDER BY attempt DESC LIMIT 1`
		args = []any{runID, nodeID}
	}
	row := d.db.QueryRow(q, args...)
	var n NodeDiffSummary
	var recordedAt string
	var filesChanged, insertions, deletions sql.NullInt64
	if err := row.Scan(&n.NodeID, &n.Attempt, &n.BeforeSHA, &n.AfterSHA,
		&filesChanged, &insertions, &deletions, &recordedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	n.RecordedAt, _ = time.Parse(time.RFC3339Nano, recordedAt)
	if filesChanged.Valid {
		v := int(filesChanged.Int64)
		n.FilesChanged = &v
	}
	if insertions.Valid {
		v := int(insertions.Int64)
		n.Insertions = &v
	}
	if deletions.Valid {
		v := int(deletions.Int64)
		n.Deletions = &v
	}
	return &n, nil
}

// NodeArtifactSummary is a read-only view of a stored node execution artifact.
type NodeArtifactSummary struct {
	ID              int64     `json:"id"`
	NodeExecutionID int64     `json:"node_execution_id"`
	Name            string    `json:"name"`
	ContentType     string    `json:"content_type"`
	SizeBytes       int64     `json:"size_bytes"`
	Truncated       bool      `json:"truncated"`
	Content         []byte    `json:"content,omitempty"`
	CapturedAt      time.Time `json:"captured_at"`
}

// GetNodeArtifactsForRunNode returns captured artifacts for a node's latest
// attempt in a run. Content is always included.
func (d *DB) GetNodeArtifactsForRunNode(runID, nodeID string) ([]NodeArtifactSummary, error) {
	return d.getNodeArtifacts(runID, nodeID, 0)
}

// GetNodeArtifactsForAttempt returns captured artifacts for a specific attempt
// of a node in a run.
func (d *DB) GetNodeArtifactsForAttempt(runID, nodeID string, attempt int) ([]NodeArtifactSummary, error) {
	return d.getNodeArtifacts(runID, nodeID, attempt)
}

func (d *DB) getNodeArtifacts(runID, nodeID string, attempt int) ([]NodeArtifactSummary, error) {
	var execID int64
	if attempt > 0 {
		row := d.db.QueryRow(`SELECT id FROM node_executions
			WHERE run_id = ? AND node_id = ? AND attempt = ?`, runID, nodeID, attempt)
		if err := row.Scan(&execID); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
	} else {
		row := d.db.QueryRow(`SELECT id FROM node_executions
			WHERE run_id = ? AND node_id = ?
			ORDER BY attempt DESC LIMIT 1`, runID, nodeID)
		if err := row.Scan(&execID); err != nil {
			if err == sql.ErrNoRows {
				return nil, nil
			}
			return nil, err
		}
	}
	rows, err := d.db.Query(`SELECT id, node_execution_id, name, content_type,
		size_bytes, truncated, content, captured_at
		FROM node_execution_artifacts
		WHERE node_execution_id = ?
		ORDER BY id ASC`, execID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeArtifactSummary
	for rows.Next() {
		var a NodeArtifactSummary
		var truncated int
		var capturedAt string
		if err := rows.Scan(&a.ID, &a.NodeExecutionID, &a.Name, &a.ContentType,
			&a.SizeBytes, &truncated, &a.Content, &capturedAt); err != nil {
			return nil, err
		}
		a.Truncated = truncated != 0
		a.CapturedAt, _ = time.Parse(time.RFC3339Nano, capturedAt)
		out = append(out, a)
	}
	return out, nil
}

// GetDotSource returns the stored DOT source for a run, if available.
func (d *DB) GetDotSource(runID string) string {
	var src string
	_ = d.db.QueryRow("SELECT COALESCE(dot_source, '') FROM runs WHERE run_id = ?", runID).Scan(&src)
	return src
}

// ReconcileStaleRuns marks runs stuck in "running" status as "interrupted"
// if they were started more than maxAge ago. Called on server startup.
func (d *DB) ReconcileStaleRuns(maxAge time.Duration) (int, error) {
	cutoff := time.Now().Add(-maxAge).UTC().Format(time.RFC3339Nano)
	result, err := d.db.Exec(`UPDATE runs SET status = 'interrupted',
		failure_reason = 'marked interrupted: process no longer running',
		completed_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE status = 'running' AND started_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (d *DB) queryRuns(clause string, args []any) ([]RunSummary, error) {
	q := `SELECT r.run_id, r.graph_name, r.goal, r.status, r.logs_root,
		r.worktree_dir, r.run_branch, r.repo_path, r.started_at, r.completed_at,
		r.duration_ms, r.final_sha, r.failure_reason, r.labels_json, r.inputs_json,
		r.warnings_json, r.invocation_json, r.config_json,
		(SELECT COUNT(*) FROM node_executions ne WHERE ne.run_id = r.run_id) as node_count
		FROM runs r ` + clause

	rows, err := d.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []RunSummary
	for rows.Next() {
		var s RunSummary
		var startedAt string
		var completedAt, finalSHA, failureReason, labelsJSON, inputsJSON, warningsJSON, invocationJSON, configJSON sql.NullString
		var durationMS sql.NullInt64
		if err := rows.Scan(&s.RunID, &s.GraphName, &s.Goal, &s.Status, &s.LogsRoot,
			&s.WorktreeDir, &s.RunBranch, &s.RepoPath, &startedAt, &completedAt,
			&durationMS, &finalSHA, &failureReason, &labelsJSON, &inputsJSON,
			&warningsJSON, &invocationJSON, &configJSON, &s.NodeCount); err != nil {
			return nil, err
		}
		s.StartedAt, _ = time.Parse(time.RFC3339Nano, startedAt)
		if completedAt.Valid {
			t, _ := time.Parse(time.RFC3339Nano, completedAt.String)
			s.CompletedAt = &t
		}
		if durationMS.Valid {
			s.DurationMS = &durationMS.Int64
		}
		s.FinalSHA = finalSHA.String
		s.FailureReason = failureReason.String
		if labelsJSON.Valid {
			_ = json.Unmarshal([]byte(labelsJSON.String), &s.Labels)
		}
		if inputsJSON.Valid {
			_ = json.Unmarshal([]byte(inputsJSON.String), &s.Inputs)
		}
		if warningsJSON.Valid {
			_ = json.Unmarshal([]byte(warningsJSON.String), &s.Warnings)
		}
		if invocationJSON.Valid {
			_ = json.Unmarshal([]byte(invocationJSON.String), &s.Invocation)
		}
		if configJSON.Valid {
			_ = json.Unmarshal([]byte(configJSON.String), &s.Config)
		}
		results = append(results, s)
	}
	return results, nil
}

// fileInfoStat wraps os.Stat for testing.
var fileInfoStat = defaultFileInfoStat

func defaultFileInfoStat(path string) (any, error) {
	return os.Stat(path)
}
