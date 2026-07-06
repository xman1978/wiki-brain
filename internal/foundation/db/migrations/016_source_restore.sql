-- Migration 016: precise restore for soft-deleted sources (文件管理 恢复按钮)

-- Snapshot of a KnowledgeUnit's lifecycle value immediately before a Source
-- soft-delete deprecates it. NULL means "never soft-deleted" (or already
-- restored). Restoring reads this back per-unit instead of blindly resetting
-- every unit to 'current' — a source's units may have held different
-- lifecycle states at delete time (some current, some already superseded by
-- an earlier reupload), and a blind restore would incorrectly resurrect the
-- superseded ones.
ALTER TABLE knowledge_units ADD COLUMN lifecycle_before_delete TEXT;
