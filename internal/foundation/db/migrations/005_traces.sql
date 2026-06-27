-- Migration 005: Trace tables - traces, cooccurrence, dedup, learning_events

CREATE TABLE IF NOT EXISTS traces (
    trace_id          TEXT PRIMARY KEY,
    answer_id         TEXT NOT NULL REFERENCES answers(answer_id),
    question          TEXT NOT NULL,
    question_hash     TEXT NOT NULL,
    question_terms    TEXT NOT NULL,
    retrieval_quality TEXT NOT NULL,
    path              TEXT NOT NULL,
    direct_point_ids  TEXT NOT NULL DEFAULT '[]',
    has_feedback      INTEGER NOT NULL DEFAULT 0,
    feedback_type     TEXT,
    feedback_content  TEXT,
    created_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_traces_answer_id ON traces(answer_id);
CREATE INDEX IF NOT EXISTS idx_traces_quality ON traces(retrieval_quality);
CREATE INDEX IF NOT EXISTS idx_traces_question_hash ON traces(question_hash);

CREATE TABLE IF NOT EXISTS question_kp_cooccurrence (
    cooc_id         TEXT PRIMARY KEY,
    question_terms  TEXT NOT NULL,
    point_id        TEXT NOT NULL REFERENCES knowledge_points(point_id),
    hit_count       INTEGER NOT NULL DEFAULT 0,
    confident_count INTEGER NOT NULL DEFAULT 0,
    last_seen_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX IF NOT EXISTS idx_cooc_point_id ON question_kp_cooccurrence(point_id);

CREATE TABLE IF NOT EXISTS cooccurrence_question_dedup (
    question_hash TEXT NOT NULL,
    point_id      TEXT NOT NULL REFERENCES knowledge_points(point_id),
    first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (question_hash, point_id)
);

CREATE TABLE IF NOT EXISTS learning_events (
    event_id    TEXT PRIMARY KEY,
    trace_id    TEXT NOT NULL REFERENCES traces(trace_id),
    event_type  TEXT NOT NULL,
    payload     TEXT NOT NULL DEFAULT '{}',
    processed   INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_learning_events_type ON learning_events(event_type);
CREATE INDEX IF NOT EXISTS idx_learning_events_processed ON learning_events(processed);
