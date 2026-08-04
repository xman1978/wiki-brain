-- Migration 030: track the concept a kind=add candidate confirm resolved to,
-- and whether that confirm created a brand-new concept row — needed to
-- support restoring an applied candidate back to pending_confirm (only the
-- new-concept path is restorable: reverting an assign-to-existing-concept
-- confirm would touch a concept this candidate didn't create).

ALTER TABLE entry_candidates ADD COLUMN resolved_entry_id TEXT REFERENCES entries(entry_id);
ALTER TABLE entry_candidates ADD COLUMN created_new_entry INTEGER NOT NULL DEFAULT 0;
