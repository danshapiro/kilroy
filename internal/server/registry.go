package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/danshapiro/kilroy/internal/attractor/engine"
	"github.com/danshapiro/kilroy/internal/attractor/runtime"
)

// PipelineState tracks a single running or completed pipeline.
type PipelineState struct {
	RunID       string
	Broadcaster *Broadcaster
	Interviewer *WebInterviewer
	Cancel      context.CancelCauseFunc
	StartedAt   time.Time
	LogsRoot    string

	mu     sync.Mutex
	eng    *engine.Engine
	result *engine.Result
	err    error
	done   bool
}

// SetEngine stores a reference to the live engine (for context inspection).
func (ps *PipelineState) SetEngine(e *engine.Engine) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.eng = e
}

// SetResult records the terminal outcome of the pipeline.
func (ps *PipelineState) SetResult(res *engine.Result, err error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.result = res
	ps.err = err
	ps.done = true
}

// Status returns the current pipeline status for the HTTP API.
func (ps *PipelineState) Status() PipelineStatus {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	status := PipelineStatus{
		RunID:    ps.RunID,
		State:    "running",
		LogsRoot: ps.LogsRoot,
	}
	if ps.done {
		if ps.err != nil {
			status.State = string(runtime.FinalFail)
			status.FailureReason = ps.err.Error()
		} else if ps.result != nil {
			status.State = string(ps.result.FinalStatus)
			status.FinalCommit = ps.result.FinalCommitSHA
			status.WorktreeDir = ps.result.WorktreeDir
			status.RunBranch = ps.result.RunBranch
			status.CXDBUIURL = ps.result.CXDBUIURL
			if ps.result.LogsRoot != "" {
				status.LogsRoot = ps.result.LogsRoot
			}
		}
	}

	// Extract current node from the latest progress event.
	if !ps.done && ps.Broadcaster != nil {
		history := ps.Broadcaster.History()
		for i := len(history) - 1; i >= 0; i-- {
			ev := history[i]
			if nid, ok := ev["node_id"].(string); ok && nid != "" {
				status.CurrentNodeID = nid
				break
			}
		}
		if len(history) > 0 {
			last := history[len(history)-1]
			if evt, ok := last["event"].(string); ok {
				status.LastEvent = evt
			}
			if ts, ok := last["ts"].(string); ok {
				if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
					status.LastEventAt = &t
				}
			}
		}
	}
	return status
}

// ContextValues returns the current engine context values, or nil if unavailable.
func (ps *PipelineState) ContextValues() map[string]any {
	ps.mu.Lock()
	eng := ps.eng
	ps.mu.Unlock()
	if eng == nil || eng.Context == nil {
		return map[string]any{}
	}
	return eng.Context.SnapshotValues()
}

// PipelineRegistry tracks all pipelines managed by this server instance.
type PipelineRegistry struct {
	mu        sync.RWMutex
	pipelines map[string]*PipelineState
}

// NewPipelineRegistry creates a new empty registry.
func NewPipelineRegistry() *PipelineRegistry {
	return &PipelineRegistry{
		pipelines: make(map[string]*PipelineState),
	}
}

// Register adds a pipeline to the registry. Returns error if ID already exists.
func (r *PipelineRegistry) Register(runID string, ps *PipelineState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.pipelines[runID]; exists {
		return fmt.Errorf("pipeline %s already exists", runID)
	}
	r.pipelines[runID] = ps
	return nil
}

// Get returns a pipeline by ID, or nil and false if not found.
func (r *PipelineRegistry) Get(runID string) (*PipelineState, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ps, ok := r.pipelines[runID]
	return ps, ok
}

// List returns all pipeline IDs.
func (r *PipelineRegistry) List() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.pipelines))
	for id := range r.pipelines {
		ids = append(ids, id)
	}
	return ids
}

// CancelAll cancels all running pipelines with the given reason.
func (r *PipelineRegistry) CancelAll(reason string) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, ps := range r.pipelines {
		if ps.Cancel != nil {
			ps.Cancel(fmt.Errorf("%s", reason))
		}
		if ps.Interviewer != nil {
			ps.Interviewer.Cancel()
		}
	}
}
