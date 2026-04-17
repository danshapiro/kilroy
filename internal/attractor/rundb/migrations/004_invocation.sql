-- Add invocation_json and config_json columns to capture how a run was triggered.
ALTER TABLE runs ADD COLUMN invocation_json TEXT NOT NULL DEFAULT '[]';
ALTER TABLE runs ADD COLUMN config_json TEXT NOT NULL DEFAULT '{}';
