-- Migration 003: knowledge_units, knowledge_points, knowledge_point_relations tables

CREATE TABLE IF NOT EXISTS knowledge_units (
    unit_id        TEXT PRIMARY KEY,
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    outline_id     TEXT REFERENCES source_outlines(outline_id),
    concept_id     TEXT REFERENCES concepts(concept_id),
    center         TEXT NOT NULL,
    line_start     INTEGER NOT NULL,
    line_end       INTEGER NOT NULL,
    status         TEXT NOT NULL DEFAULT 'pending',
    error_msg      TEXT,
    prompt_version TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_knowledge_units_source_id ON knowledge_units(source_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_units_outline_id ON knowledge_units(outline_id);

CREATE TABLE IF NOT EXISTS knowledge_points (
    point_id       TEXT PRIMARY KEY,
    unit_id        TEXT NOT NULL REFERENCES knowledge_units(unit_id),
    source_id      TEXT NOT NULL REFERENCES sources(source_id),
    content        TEXT NOT NULL,
    point_type     TEXT NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_knowledge_points_unit_id ON knowledge_points(unit_id);
CREATE INDEX IF NOT EXISTS idx_knowledge_points_source_id ON knowledge_points(source_id);

CREATE TABLE IF NOT EXISTS knowledge_point_relations (
    relation_id      TEXT PRIMARY KEY,
    source_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    target_point_id  TEXT NOT NULL REFERENCES knowledge_points(point_id),
    relation_type    TEXT NOT NULL,
    direction        TEXT NOT NULL DEFAULT 'directed',
    prompt_version   TEXT NOT NULL,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_kp_relations_source ON knowledge_point_relations(source_point_id);
CREATE INDEX IF NOT EXISTS idx_kp_relations_target ON knowledge_point_relations(target_point_id);
