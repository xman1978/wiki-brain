-- Migration 001: Initial schema - domains and concepts tables

CREATE TABLE IF NOT EXISTS domains (
    domain_id    TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS concepts (
    concept_id   TEXT PRIMARY KEY,
    domain_id    TEXT NOT NULL REFERENCES domains(domain_id),
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_concepts_domain_id ON concepts(domain_id);
