-- Migration 010: KU/KP lifecycle state (current/superseded/deprecated) +
-- Shadow Source columns for reupload (docs/impl/v1/lifecycle.md)

ALTER TABLE knowledge_units  ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'current';
ALTER TABLE knowledge_points ADD COLUMN lifecycle TEXT NOT NULL DEFAULT 'current';
ALTER TABLE knowledge_units  ADD COLUMN lifecycle_changed_at DATETIME;
ALTER TABLE knowledge_points ADD COLUMN lifecycle_changed_at DATETIME;

CREATE INDEX IF NOT EXISTS idx_ku_lifecycle ON knowledge_units(lifecycle);
CREATE INDEX IF NOT EXISTS idx_kp_lifecycle ON knowledge_points(lifecycle);

-- shadow_of non-null marks a hidden Source created for POST /sources/:id/reupload;
-- excluded from GET /sources, Domain 预过滤, Source 语义过滤 until swap completes.
ALTER TABLE sources ADD COLUMN shadow_of TEXT REFERENCES sources(source_id);
CREATE INDEX IF NOT EXISTS idx_sources_shadow_of ON sources(shadow_of);

-- sources.status enum extended with 'deleted' (soft delete, no CHECK constraint
-- exists at the SQL level for this column, see docs/impl/v1/lifecycle.md 步骤 2).
