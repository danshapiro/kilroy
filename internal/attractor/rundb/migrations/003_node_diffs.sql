-- Per-node git diff tracking.
-- Records before/after SHAs for each node execution in a git-managed workspace.

CREATE TABLE IF NOT EXISTS node_diffs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id        TEXT NOT NULL REFERENCES runs(run_id) ON DELETE CASCADE,
    node_id       TEXT NOT NULL,
    attempt       INTEGER NOT NULL DEFAULT 1,
    before_sha    TEXT NOT NULL,
    after_sha     TEXT NOT NULL,
    files_changed INTEGER,
    insertions    INTEGER,
    deletions     INTEGER,
    recorded_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_node_diffs_run ON node_diffs(run_id);
CREATE INDEX IF NOT EXISTS idx_node_diffs_node ON node_diffs(run_id, node_id);
