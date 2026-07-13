-- Migration 021: units_completed_at records when knowledge-unit extraction
-- (units_status, see migration 018) actually reached a terminal state
-- (completed/failed). Previously the file-management timeline reused
-- sources.completed_at (source processing's own completion time) to display
-- this stage, which is wrong once extraction takes any noticeable time after
-- processing finishes.

ALTER TABLE sources ADD COLUMN units_completed_at DATETIME;
