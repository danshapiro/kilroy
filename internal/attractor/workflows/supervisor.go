// Run supervisor: monitors active runs and classifies their health state.
// Queries the RunDB to detect stuck, degraded, or blocked runs.
package workflows

import (
	"fmt"
	"strings"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/rundb"
)

// RunHealth classifies the operational state of a run.
type RunHealth string

const (
	HealthHealthy  RunHealth = "healthy"
	HealthDegraded RunHealth = "degraded"
	HealthBlocked  RunHealth = "blocked"
	HealthFailed   RunHealth = "failed"
	HealthComplete RunHealth = "complete"
	HealthUnknown  RunHealth = "unknown"
)

// RunAssessment is the supervisor's evaluation of a single run.
type RunAssessment struct {
	RunID       string
	GraphName   string
	Health      RunHealth
	Reason      string
	Status      string
	StartedAt   time.Time
	DurationMS  int64
	NodeTimings []NodeTiming
}

// NodeTiming summarizes a single node execution for status display.
type NodeTiming struct {
	NodeID      string
	HandlerType string
	Status      string
	DurationMS  int64
	Attempt     int
}

// AssessRun evaluates the health of a single run from RunDB data.
func AssessRun(db *rundb.DB, runID string) (*RunAssessment, error) {
	run, err := db.GetRun(runID)
	if err != nil {
		return nil, fmt.Errorf("get run %s: %w", runID, err)
	}
	if run == nil {
		return nil, fmt.Errorf("run %s not found", runID)
	}

	nodes, err := db.GetNodeExecutions(runID)
	if err != nil {
		return nil, fmt.Errorf("get node executions for %s: %w", runID, err)
	}

	a := &RunAssessment{
		RunID:     run.RunID,
		GraphName: run.GraphName,
		Status:    run.Status,
		StartedAt: run.StartedAt,
	}
	if run.DurationMS != nil {
		a.DurationMS = *run.DurationMS
	}

	// Build per-node timing.
	for _, n := range nodes {
		nt := NodeTiming{
			NodeID:      n.NodeID,
			HandlerType: n.HandlerType,
			Status:      n.Status,
			Attempt:     n.Attempt,
		}
		if n.DurationMS != nil {
			nt.DurationMS = *n.DurationMS
		}
		a.NodeTimings = append(a.NodeTimings, nt)
	}

	// Classify health.
	a.Health, a.Reason = classifyHealth(run, nodes)
	return a, nil
}

// AssessActiveRuns evaluates all currently running runs.
func AssessActiveRuns(db *rundb.DB) ([]RunAssessment, error) {
	runs, err := db.ListRuns(rundb.ListFilter{Status: "running"})
	if err != nil {
		return nil, err
	}
	var assessments []RunAssessment
	for _, run := range runs {
		a, err := AssessRun(db, run.RunID)
		if err != nil {
			continue
		}
		assessments = append(assessments, *a)
	}
	return assessments, nil
}

func classifyHealth(run *rundb.RunSummary, nodes []rundb.NodeExecutionSummary) (RunHealth, string) {
	switch strings.ToLower(run.Status) {
	case "success":
		return HealthComplete, "run completed successfully"
	case "fail", "failed":
		return HealthFailed, fmt.Sprintf("run failed: %s", run.FailureReason)
	case "canceled":
		return HealthFailed, "run was canceled"
	}

	// Running — check for degradation signals.
	if len(nodes) == 0 {
		return HealthHealthy, "no nodes executed yet"
	}

	// Check for repeated failures on the same node (blocked).
	failCounts := map[string]int{}
	for _, n := range nodes {
		if strings.EqualFold(n.Status, "fail") || strings.EqualFold(n.Status, "retry") {
			failCounts[n.NodeID]++
		}
	}
	for nodeID, count := range failCounts {
		if count >= 3 {
			return HealthBlocked, fmt.Sprintf("node %s has failed %d times", nodeID, count)
		}
	}

	// Check for stale progress (no node completed recently).
	hasRetries := false
	for _, n := range nodes {
		if n.Attempt > 1 {
			hasRetries = true
			break
		}
	}
	if hasRetries {
		return HealthDegraded, "run is retrying failed nodes"
	}

	// Check for stall: running but no node completion in recent window.
	lastCompletion := run.StartedAt
	for _, n := range nodes {
		if n.CompletedAt != nil && n.CompletedAt.After(lastCompletion) {
			lastCompletion = *n.CompletedAt
		}
	}
	stallThreshold := 10 * time.Minute
	if time.Since(lastCompletion) > stallThreshold {
		return HealthBlocked, fmt.Sprintf("no progress for %s", time.Since(lastCompletion).Round(time.Second))
	}

	return HealthHealthy, "running normally"
}

// FormatAssessment returns a human-readable summary of a run assessment.
func FormatAssessment(a *RunAssessment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "run=%s graph=%s health=%s status=%s\n", a.RunID, a.GraphName, a.Health, a.Status)
	if a.Reason != "" {
		fmt.Fprintf(&b, "  reason: %s\n", a.Reason)
	}
	if a.DurationMS > 0 {
		fmt.Fprintf(&b, "  duration: %dms\n", a.DurationMS)
	}
	if len(a.NodeTimings) > 0 {
		fmt.Fprintf(&b, "  nodes:\n")
		for _, nt := range a.NodeTimings {
			attempt := ""
			if nt.Attempt > 1 {
				attempt = fmt.Sprintf(" (attempt %d)", nt.Attempt)
			}
			fmt.Fprintf(&b, "    %-20s %-12s %6dms %s%s\n", nt.NodeID, nt.HandlerType, nt.DurationMS, nt.Status, attempt)
		}
	}
	return b.String()
}
