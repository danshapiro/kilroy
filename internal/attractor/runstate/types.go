package runstate

import "time"

type State string

const (
	StateUnknown State = "unknown"
	StateRunning State = "running"
	StateSuccess State = "success"
	StateFail    State = "fail"
)

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
}
