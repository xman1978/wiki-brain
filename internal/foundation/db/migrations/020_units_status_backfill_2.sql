-- Migration 020: second backfill pass for units_status.
--
-- Root cause of why 019 wasn't enough: the queue handler that was supposed
-- to write units_status (unit.Service.RegisterHandler) was never actually
-- wired up by cmd/server/main.go — main.go registers its own separate
-- inline closure for TaskTypeUnitExtract that calls unitSvc.Extract()
-- directly (plus CompleteShadowSwap, which RegisterHandler didn't even
-- know about), bypassing RegisterHandler entirely. So every extraction that
-- ran between 018/019 being applied and this fix landing in main.go
-- finished successfully but left units_status stuck on 'pending' — the same
-- symptom 019 backfilled, just for rows that completed after 019 already
-- ran. Re-running the identical backfill catches those stragglers; it's a
-- no-op for anything 019 (or the now-fixed main.go handler) already covered.

UPDATE sources
SET units_status = 'completed'
WHERE status = 'completed'
  AND units_status != 'completed'
  AND source_id IN (SELECT DISTINCT source_id FROM knowledge_units);
