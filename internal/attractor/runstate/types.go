package runstate

import "time"

type State string

const (
	StateUnknown State = "unknown"
	StateRunning State = "running"
	StateSuccess State = "success"
	StateFail    State = "fail"
)

type StageAttempt struct {
	NodeID        string `json:"node_id"`
	Status        string `json:"status"`
	Attempt       int    `json:"attempt"`
	MaxAttempts   int    `json:"max_attempts"`
	FailureReason string `json:"failure_reason,omitempty"`
}

type EdgeTransition struct {
	From      string `json:"from"`
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

type Snapshot struct {
	LogsRoot      string    `json:"logs_root"`
	RunID         string    `json:"run_id,omitempty"`
	State         State     `json:"state"`
	CurrentNodeID string    `json:"current_node_id,omitempty"`
	LastEvent     string    `json:"last_event,omitempty"`
	LastEventAt   time.Time `json:"last_event_at,omitempty"`
	FailureReason string    `json:"failure_reason,omitempty"`
	PID           int       `json:"pid,omitempty"`
	PIDAlive      bool      `json:"pid_alive"`

	// Verbose fields (populated only when requested via ApplyVerbose)
	FinalCommitSHA string           `json:"final_commit_sha,omitempty"`
	CXDBContextID  string           `json:"cxdb_context_id,omitempty"`
	CompletedNodes []string         `json:"completed_nodes,omitempty"`
	RetryCounts    map[string]int   `json:"retry_counts,omitempty"`
	StageTrace     []StageAttempt   `json:"stage_trace,omitempty"`
	EdgeTrace      []EdgeTransition `json:"edge_trace,omitempty"`
	PostmortemText string           `json:"postmortem_text,omitempty"`
	ReviewText     string           `json:"review_text,omitempty"`
}
