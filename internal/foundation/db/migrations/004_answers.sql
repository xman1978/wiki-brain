CREATE TABLE IF NOT EXISTS answers (
    answer_id         TEXT PRIMARY KEY,
    question          TEXT NOT NULL,
    content           TEXT NOT NULL,
    citations         TEXT NOT NULL DEFAULT '[]',
    evidence_snapshot TEXT NOT NULL DEFAULT '{}',
    has_answer        INTEGER NOT NULL DEFAULT 1,
    path              TEXT NOT NULL,
    prompt_version    TEXT NOT NULL,
    model_name        TEXT NOT NULL,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
