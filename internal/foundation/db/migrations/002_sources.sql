-- Migration 002: sources and source_outlines tables

CREATE TABLE IF NOT EXISTS sources (
    source_id     TEXT PRIMARY KEY,
    title         TEXT NOT NULL,
    format        TEXT NOT NULL,
    file_name     TEXT NOT NULL,
    original_path TEXT NOT NULL,
    html_path     TEXT,
    markdown_path TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'pending',
    error_msg     TEXT,
    outline_type  TEXT,
    summary       TEXT,
    domain_id     TEXT REFERENCES domains(domain_id),
    word_count    INTEGER,
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS source_outlines (
    outline_id  TEXT PRIMARY KEY,
    source_id   TEXT NOT NULL REFERENCES sources(source_id),
    parent_id   TEXT REFERENCES source_outlines(outline_id),
    level       INTEGER NOT NULL,
    title       TEXT NOT NULL,
    summary     TEXT,
    line_start  INTEGER NOT NULL,
    line_end    INTEGER NOT NULL,
    node_type   TEXT NOT NULL DEFAULT 'structural',
    position    INTEGER NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_source_outlines_source_id ON source_outlines(source_id);
CREATE INDEX IF NOT EXISTS idx_source_outlines_parent_id ON source_outlines(parent_id);
