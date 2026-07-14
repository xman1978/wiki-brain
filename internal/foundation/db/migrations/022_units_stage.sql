-- Migration 022: unit substage progress for the file management timeline.
-- units_stage splits the existing unit extraction status into build vs
-- rerank semantic phases; units_built_at records the transition time.

ALTER TABLE sources ADD COLUMN units_stage TEXT NOT NULL DEFAULT 'pending';
ALTER TABLE sources ADD COLUMN units_built_at DATETIME;
