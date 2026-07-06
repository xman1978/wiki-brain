-- Migration 014: Wiki compilation (docs/impl/v1/wiki.md)

CREATE TABLE wiki_pages (
    page_id          TEXT PRIMARY KEY,
    page_type        TEXT NOT NULL,
    -- topic / concept（V1 两种；编译输入相同，区别在标题组织）
    concept_id       TEXT REFERENCES concepts(concept_id),
    title            TEXT NOT NULL,
    content          TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'draft',
    -- draft / published / needs_recompile / archived
    source_point_ids TEXT NOT NULL DEFAULT '[]',
    source_unit_ids  TEXT NOT NULL DEFAULT '[]',
    compiled_from    TEXT NOT NULL DEFAULT '[]',
    prompt_version   TEXT NOT NULL,
    model_name       TEXT NOT NULL,
    compiled_at      DATETIME,
    published_at     DATETIME,
    created_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_status  ON wiki_pages(status);
CREATE INDEX idx_wiki_concept ON wiki_pages(concept_id);

CREATE TABLE wiki_revisions (
    revision_id  TEXT PRIMARY KEY,
    page_id      TEXT NOT NULL REFERENCES wiki_pages(page_id),
    content      TEXT NOT NULL,
    reason       TEXT NOT NULL,
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_rev_page ON wiki_revisions(page_id);
