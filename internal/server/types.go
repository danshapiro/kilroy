package server

import "time"

// SubmitPipelineRequest is the POST /pipelines request body.
// Supports two modes: (1) legacy dot_source + config_path, or
// (2) workflow package reference with workspace and inputs.
type SubmitPipelineRequest struct {
	// --- Mode 1: Legacy (dot source + config file) ---

	// DotSource is the pipeline graph in DOT format (inline).
	DotSource string `json:"dot_source,omitempty"`

	// DotSourcePath is a filesystem path to the DOT file.
	DotSourcePath string `json:"dot_source_path,omitempty"`

	// ConfigPath is a filesystem path to the run config YAML.
	ConfigPath string `json:"config_path,omitempty"`

	// --- Mode 2: Workflow package ---

	// Workflow is the name of a workflow package (e.g. "pr-review").
	// Resolved from the workflows/ directory.
	Workflow string `json:"workflow,omitempty"`

	// PackagePath is an explicit filesystem path to a workflow package directory.
	// Takes precedence over Workflow name-based resolution.
	PackagePath string `json:"package_path,omitempty"`

	// Workspace is the directory to execute in.
	Workspace string `json:"workspace,omitempty"`

	// Inputs are structured key-value pairs passed to the graph.
	Inputs map[string]any `json:"inputs,omitempty"`

	// Labels are key-value pairs for tagging the run.
	Labels map[string]string `json:"labels,omitempty"`

	// --- Common options ---

	// RunID is optional. If empty, a ULID is generated.
	RunID string `json:"run_id,omitempty"`

	// Tmux enables tmux-based agent execution.
	Tmux bool `json:"tmux,omitempty"`

	// ForceModels maps provider -> model for overrides.
	ForceModels map[string]string `json:"force_models,omitempty"`

	// AllowTestShim enables test shim mode.
	AllowTestShim bool `json:"allow_test_shim,omitempty"`
}

// PipelineStatus is returned by GET /pipelines/{id}.
type PipelineStatus struct {
	RunID         string     `json:"run_id"`
	State         string     `json:"state"`
	CurrentNodeID string     `json:"current_node_id,omitempty"`
	LastEvent     string     `json:"last_event,omitempty"`
	LastEventAt   *time.Time `json:"last_event_at,omitempty"`
	FailureReason string     `json:"failure_reason,omitempty"`
	LogsRoot      string     `json:"logs_root,omitempty"`
	WorktreeDir   string     `json:"worktree_dir,omitempty"`
	RunBranch     string     `json:"run_branch,omitempty"`
	FinalCommit   string     `json:"final_commit,omitempty"`
	CXDBUIURL     string     `json:"cxdb_ui_url,omitempty"`
}

// PendingQuestion is returned by GET /pipelines/{id}/questions.
type PendingQuestion struct {
	QuestionID string           `json:"question_id"`
	Type       string           `json:"type"`
	Text       string           `json:"text"`
	Stage      string           `json:"stage"`
	Options    []QuestionOption `json:"options,omitempty"`
	AskedAt    time.Time        `json:"asked_at"`
}

// QuestionOption is a single option in a human gate question.
type QuestionOption struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	To    string `json:"to,omitempty"`
}

// AnswerRequest is the POST /pipelines/{id}/questions/{qid}/answer body.
type AnswerRequest struct {
	Value  string   `json:"value,omitempty"`
	Values []string `json:"values,omitempty"`
	Text   string   `json:"text,omitempty"`
}

// ErrorResponse is a standard error envelope.
type ErrorResponse struct {
	Error   string `json:"error"`
	Details string `json:"details,omitempty"`
}
