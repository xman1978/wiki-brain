-- Migration 007: add processing/completed timestamps to sources

ALTER TABLE sources ADD COLUMN processing_started_at DATETIME;
ALTER TABLE sources ADD COLUMN completed_at DATETIME;
