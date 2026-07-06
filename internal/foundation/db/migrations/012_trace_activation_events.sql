-- Migration 012: Trace V1 extension (docs/impl/v1/trace.md) — path_type,
-- activation_link_ids, and the Session four-tuple columnized so Study can
-- query them directly when creating ActivationLinks.

ALTER TABLE traces ADD COLUMN path_type           TEXT NOT NULL DEFAULT 'full';
ALTER TABLE traces ADD COLUMN activation_link_ids  TEXT NOT NULL DEFAULT '[]';
ALTER TABLE traces ADD COLUMN subject              TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN intent               TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN audience             TEXT NOT NULL DEFAULT '';
ALTER TABLE traces ADD COLUMN constraint_text      TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_traces_path_type ON traces(path_type);
