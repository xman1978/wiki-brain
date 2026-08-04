-- Migration 031: track the KPN cross-Source relation_ids created by a
-- kind=add candidate confirm's directed rematch (docs/impl/v1/kpn.md 步骤 6),
-- so restoring an applied candidate back to pending_confirm can clean up
-- exactly those relations (and only those — not relations created later by
-- unrelated Source imports that happen to also touch these points once they
-- have an entry_id).

ALTER TABLE entry_candidates ADD COLUMN kpn_relation_ids TEXT NOT NULL DEFAULT '[]';
