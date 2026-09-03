-- Migration 075: doc_categories — per-domain predefined document genre
-- taxonomy (docs/design/doc-category.md, docs/impl/v1/doc-category.md).
-- Independent of the emergent, query-driven source_affinity "主题标签"
-- mechanism: this is a closed, human-curated enum scoped by domain_id.

CREATE TABLE IF NOT EXISTS doc_categories (
    category_id TEXT PRIMARY KEY,
    domain_id   TEXT NOT NULL REFERENCES domains(domain_id),
    name        TEXT NOT NULL,
    description TEXT,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_doc_categories_domain_id ON doc_categories(domain_id);

ALTER TABLE sources ADD COLUMN doc_category_id TEXT REFERENCES doc_categories(category_id);
