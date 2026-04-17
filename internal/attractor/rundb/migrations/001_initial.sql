-- Initial schema for the kilroy run database.
-- Stores run-level operational state: runs, node executions, outcomes, edge decisions.

CREATE TABLE IF NOT EXISTS runs (
    run_id       TEXT PRIMARY KEY,
    graph_name   TEXT NOT NULL DEFAULT '',
    goal         TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'running', -- running, success, fail, canceled
    logs_root    TEXT NOT NULL DEFAULT '',
    worktree_dir TEXT NOT NULL DEFAULT '',
    run_branch   TEXT NOT NULL DEFAULT '',
    repo_path    TEXT NOT NULL DEFAULT '',
    started_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT,
    duration_ms  INTEGER,
    dot_source   TEXT,
    inputs_json  TEXT,  -- JSON object of input key-value pairs
    labels_json  TEXT,  -- JSON object of label key-value pairs
    final_sha    TEXT,
    failure_reason TEXT,
    warnings_json  TEXT  -- JSON array of warning strings
);

CREATE TABLE IF NOT EXISTS node_executions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    node_id      TEXT NOT NULL,
    attempt      INTEGER NOT NULL DEFAULT 1,
    handler_type TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT '',  -- success, fail, retry, skipped, etc.
    started_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    completed_at TEXT,
    duration_ms  INTEGER,
    failure_reason   TEXT,
    failure_class    TEXT,
    preferred_label  TEXT,
    context_updates_json TEXT,  -- JSON object
    notes        TEXT
);
CREATE INDEX IF NOT EXISTS idx_node_executions_run ON node_executions(run_id);
CREATE INDEX IF NOT EXISTS idx_node_executions_node ON node_executions(run_id, node_id);

CREATE TABLE IF NOT EXISTS edge_decisions (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    from_node    TEXT NOT NULL,
    to_node      TEXT NOT NULL,
    edge_label   TEXT NOT NULL DEFAULT '',
    reason       TEXT NOT NULL DEFAULT '',  -- condition_match, preferred_label, suggested, weight, lexical
    decided_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_edge_decisions_run ON edge_decisions(run_id);

CREATE TABLE IF NOT EXISTS provider_selections (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id       TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    node_id      TEXT NOT NULL,
    attempt      INTEGER NOT NULL DEFAULT 1,
    provider     TEXT NOT NULL DEFAULT '',
    model        TEXT NOT NULL DEFAULT '',
    backend      TEXT NOT NULL DEFAULT '',  -- api, cli
    selected_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_provider_selections_run ON provider_selections(run_id);
