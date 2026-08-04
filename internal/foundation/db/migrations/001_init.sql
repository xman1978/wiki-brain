-- Migration 001: Initial schema - domains and entries tables

CREATE TABLE IF NOT EXISTS domains (
    domain_id    TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS entries (
    entry_id   TEXT PRIMARY KEY,
    domain_id    TEXT NOT NULL REFERENCES domains(domain_id),
    name         TEXT NOT NULL,
    description  TEXT,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_entries_domain_id ON entries(domain_id);
