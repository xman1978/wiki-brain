ALTER TABLE entries ADD COLUMN parent_entry_id TEXT REFERENCES entries(entry_id);
ALTER TABLE entry_candidates ADD COLUMN parent_entry_id TEXT REFERENCES entries(entry_id);

CREATE INDEX idx_entries_parent ON entries(parent_entry_id);
