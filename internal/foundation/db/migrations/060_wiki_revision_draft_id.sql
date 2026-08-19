-- Migration 060: wiki_revisions.draft_id
--
-- SyncDraftToPage inserts a NEW revision on every save (not an update to an
-- existing one), so a draft saved N times has accumulated N draft_sync
-- revisions by the time it's deleted — but Service.DeleteDraft could only
-- find the *latest* one via wiki_drafts.source_revision_id (which is
-- repointed to the newest revision on every save). Earlier saves' revisions
-- had no way to be traced back to the draft that produced them, so they were
-- silently left behind as orphans when the draft was deleted. draft_id lets
-- DeleteDraft find and remove all of a draft's own revisions, not just the
-- last one.
--
-- No REFERENCES/FK here on purpose: wiki_drafts.source_revision_id already
-- points the other way (draft -> its latest revision), so a FK on this
-- column would create a delete-order cycle between the two tables. This
-- column is bookkeeping for Service.DeleteDraft's own cleanup logic, not a
-- relational-integrity requirement the database needs to enforce.
ALTER TABLE wiki_revisions ADD COLUMN draft_id TEXT;

CREATE INDEX idx_wiki_rev_draft ON wiki_revisions(draft_id);
