-- P0 quality gates for Wiki compilation: claim-level support verification
-- and pre-publish self-check replay. See docs/impl/v1/wiki-generation.md
-- 阶段 E/G and docs/design/wiki-compilation.md "编译产物的支持度核验" /
-- "编译产物的发布前验收".

-- Per-claim verdict from a post-compile independent check of whether the
-- claim's text is actually supported by the KP material it cites — distinct
-- from the existing citation whitelist check, which only verifies the cited
-- point_ids are in-bounds, not that they support the claim's content.
CREATE TABLE wiki_claim_checks (
    check_id        TEXT PRIMARY KEY,
    page_id         TEXT NOT NULL REFERENCES wiki_pages(page_id),
    revision_id     TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    claim_id        TEXT NOT NULL,
    claim_text      TEXT NOT NULL,
    cited_point_ids TEXT NOT NULL DEFAULT '[]',
    verdict         TEXT NOT NULL,
    -- supported / partial / unsupported
    reason          TEXT NOT NULL DEFAULT '',
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wcc_page ON wiki_claim_checks(page_id, revision_id);

-- Pre-publish self-check: replay real confident questions this page's
-- qualifying KPs were once answered from against the compiled page itself
-- (reusing the existing answer_wiki path), and record whether the page
-- can still answer them plus material-usage/citation-density metrics.
CREATE TABLE wiki_quality_checks (
    qc_id       TEXT PRIMARY KEY,
    page_id     TEXT NOT NULL REFERENCES wiki_pages(page_id),
    revision_id TEXT NOT NULL REFERENCES wiki_revisions(revision_id),
    metrics     TEXT NOT NULL DEFAULT '{}',
    passed      INTEGER NOT NULL DEFAULT 0,
    forced      INTEGER NOT NULL DEFAULT 0,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_wqc_page ON wiki_quality_checks(page_id, revision_id);
