-- Two-tier Wiki architecture: page relations, drafts, member_roles/uncovered_points,
-- sources reflow-origin tracking, traces skeleton page reference.
-- See docs/impl/v1/wiki.md "数据结构" 两层架构扩展 and docs/impl/v1/two-tier-task-brief.md.

CREATE TABLE wiki_page_relations (
    relation_id   TEXT PRIMARY KEY,
    from_page_id  TEXT NOT NULL REFERENCES wiki_pages(page_id),
    to_page_id    TEXT NOT NULL REFERENCES wiki_pages(page_id),
    relation_type TEXT NOT NULL,
    derived_from  TEXT NOT NULL,
    evidence      TEXT NOT NULL DEFAULT '{}',
    created_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE UNIQUE INDEX idx_wpr_uniq ON wiki_page_relations(from_page_id, to_page_id, relation_type);
CREATE INDEX idx_wpr_to ON wiki_page_relations(to_page_id, relation_type);

CREATE TABLE wiki_drafts (
    draft_id           TEXT PRIMARY KEY,
    page_id            TEXT NOT NULL REFERENCES wiki_pages(page_id),
    source_revision_id TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    source_page_ids    TEXT NOT NULL DEFAULT '[]',
    evidence_index     TEXT NOT NULL DEFAULT '[]',
    title              TEXT NOT NULL,
    content            TEXT NOT NULL,
    note               TEXT NOT NULL DEFAULT '',
    created_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at         DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wiki_drafts_page ON wiki_drafts(page_id);

ALTER TABLE wiki_pages ADD COLUMN member_roles TEXT NOT NULL DEFAULT '[]';
ALTER TABLE wiki_pages ADD COLUMN uncovered_points TEXT NOT NULL DEFAULT '[]';

ALTER TABLE sources ADD COLUMN origin TEXT NOT NULL DEFAULT 'upload';
ALTER TABLE sources ADD COLUMN origin_page_id TEXT REFERENCES wiki_pages(page_id);
ALTER TABLE sources ADD COLUMN reflow_skipped_edges INTEGER NOT NULL DEFAULT 0;
-- Observability counter for the self-ancestor KPN exclusion
-- (docs/impl/v1/kpn.md 步骤 2 "自体祖先排除"; docs/impl/v1/study.md 步骤 6
-- "wiki_draft_reflow" report item).

ALTER TABLE traces ADD COLUMN skeleton_page_id TEXT REFERENCES wiki_pages(page_id);

-- Pre-existing rows tagged page_type='topic' with a non-null concept_id were
-- produced by the old single-tier compile — semantically they are concept
-- pages; rewrite so page_type's meaning ("topic = second-tier compile output,
-- concept_id always NULL") holds for all rows going forward.
UPDATE wiki_pages SET page_type = 'concept' WHERE page_type = 'topic' AND concept_id IS NOT NULL;
