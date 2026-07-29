-- Subject synonym dictionary for ActivationLink Match's subject dimension
-- (docs/superpowers/specs/2026-07-24-activation-subject-synonym-design.md).
-- Only the subject dimension is canonicalized; intent/audience/constraint
-- keep exact-match semantics untouched.
CREATE TABLE IF NOT EXISTS subject_synonyms (
    synonym_id   TEXT PRIMARY KEY,
    domain_id    TEXT REFERENCES domains(domain_id),
    term         TEXT NOT NULL,
    -- normalized raw phrasing (text.Normalize output, phrase-level, not tokenized)
    canonical    TEXT NOT NULL,
    -- normalized phrase term canonicalizes to
    source       TEXT NOT NULL DEFAULT 'manual',
    -- preset / gap_mined / manual
    status       TEXT NOT NULL DEFAULT 'active',
    -- active / candidate / rejected
    created_from TEXT NOT NULL DEFAULT '[]',
    -- JSON array of learning_event event_id (gap_mined candidates); '[]' for preset
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_subject_synonyms_term_active
    ON subject_synonyms(term) WHERE status = 'active';
CREATE INDEX IF NOT EXISTS idx_subject_synonyms_status ON subject_synonyms(status);
