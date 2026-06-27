-- Migration 006: Study tables - link_candidates, knowledge_gaps, study_reports

CREATE TABLE IF NOT EXISTS link_candidates (
    candidate_id    TEXT PRIMARY KEY,
    question_terms  TEXT NOT NULL,
    point_id        TEXT NOT NULL REFERENCES knowledge_points(point_id),
    confident_count INTEGER NOT NULL DEFAULT 0,
    hit_count       INTEGER NOT NULL DEFAULT 0,
    flagged_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms, point_id)
);

CREATE INDEX IF NOT EXISTS idx_lc_point_id ON link_candidates(point_id);

CREATE TABLE IF NOT EXISTS knowledge_gaps (
    gap_id         TEXT PRIMARY KEY,
    question_terms TEXT NOT NULL,
    question       TEXT NOT NULL,
    hit_count      INTEGER NOT NULL DEFAULT 1,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms)
);

CREATE TABLE IF NOT EXISTS study_reports (
    report_id    TEXT PRIMARY KEY,
    period_days  INTEGER NOT NULL DEFAULT 30,
    content      TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
