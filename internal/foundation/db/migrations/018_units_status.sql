-- Migration 018: units_status tracks knowledge-unit extraction progress
-- separately from sources.status. sources.status only reflects source
-- processing (parse/outline); unit_extract is a separate async queue task
-- enqueued right after sources.status flips to completed (see
-- source/service.go's processSource), so a client polling sources.status
-- alone sees "completed" well before extraction has even started, let alone
-- finished. units_status is written by unit.Service's queue handler:
-- pending (not yet started) -> processing -> completed/failed.

ALTER TABLE sources ADD COLUMN units_status TEXT NOT NULL DEFAULT 'pending';
