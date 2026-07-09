-- Migration 019: backfill units_status for rows that existed before
-- migration 018 added the column. ALTER TABLE ... DEFAULT 'pending' set
-- every pre-existing row to 'pending' regardless of how long ago its
-- extraction actually finished, which made the file-management page show
-- long-completed sources as "处理中" again. A source whose knowledge_units
-- extraction actually ran has at least one knowledge_units row (even a
-- partial/failed extraction inserts extraction_failed placeholder rows —
-- see internal/unit/service.go's insertFailedUnit), so presence of any row
-- is a reliable signal the queue handler already completed for it.

UPDATE sources
SET units_status = 'completed'
WHERE status = 'completed'
  AND source_id IN (SELECT DISTINCT source_id FROM knowledge_units);
