-- Migration 057: Wiki 单层化改造 -- entry_id 多值承载
-- docs/impl/v1/wiki-single-tier-open-questions.md「已拍板（2026-08-18）」第 1 条
--
-- CompileRequest.EntryIDs 支持一次编译传入多个 Concept/Fact entry_id，但
-- wiki_pages.entry_id 是单值列，只能存第一个作为"主 entry"。新增关联表
-- 承载完整集合，供 Recompile 重建 Core/Context/Conflict 子图、以及后续
-- entry_id -> page_id 反查使用。wiki_pages.entry_id 单值列保留不删（catalog
-- 现有按 domain 分组的 JOIN 逻辑继续用它）。

CREATE TABLE wiki_page_entries (
    page_id  TEXT NOT NULL REFERENCES wiki_pages(page_id),
    entry_id TEXT NOT NULL REFERENCES entries(entry_id),
    PRIMARY KEY (page_id, entry_id)
);
CREATE INDEX idx_wiki_page_entries_entry ON wiki_page_entries(entry_id);
