-- Store node execution artifacts (prompts, responses, agent conversation logs,
-- tool stdout/stderr, status files, output files) in the database so loop
-- iterations and retries don't lose history when stage directories are reused
-- or filesystems are cleaned up.
CREATE TABLE IF NOT EXISTS node_execution_artifacts (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    node_execution_id   INTEGER NOT NULL REFERENCES node_executions(id) ON DELETE CASCADE,
    name                TEXT NOT NULL,
    content_type        TEXT NOT NULL DEFAULT 'application/octet-stream',
    size_bytes          INTEGER NOT NULL DEFAULT 0,
    truncated           INTEGER NOT NULL DEFAULT 0,
    content             BLOB NOT NULL,
    captured_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))
);
CREATE INDEX IF NOT EXISTS idx_node_execution_artifacts_exec ON node_execution_artifacts(node_execution_id);
CREATE INDEX IF NOT EXISTS idx_node_execution_artifacts_name ON node_execution_artifacts(node_execution_id, name);
