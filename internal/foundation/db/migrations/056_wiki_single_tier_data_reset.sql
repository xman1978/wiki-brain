-- Migration 056: Wiki 单层化改造 -- 存量数据清理
-- docs/design/wiki-single-tier-revision.md / docs/impl/v1/wiki-single-tier-task-brief.md 步骤 0
--
-- 两层架构（概念页 + 主题页）与四元组直答匹配的存量已发布数据一律不迁移，
-- 直接清空。只删数据，不删表结构——新的单层编译逻辑仍复用这些表。
-- 按外键依赖顺序：wiki_claim_checks/wiki_quality_checks/wiki_drafts 三张表
-- 同时引用 wiki_pages.page_id 与 wiki_revisions.revision_id，必须先于
-- wiki_revisions 删除，否则 SQLite 外键检查会在删除 wiki_revisions 时
-- 发现仍有子行引用而失败；wiki_page_relations 只引用 wiki_pages，删除
-- 顺序在 wiki_pages 之前即可，与 wiki_revisions 无先后要求。最后清
-- wiki_pages 本身。

DELETE FROM wiki_claim_checks;
DELETE FROM wiki_quality_checks;
DELETE FROM wiki_drafts;
DELETE FROM wiki_page_relations;
DELETE FROM wiki_revisions;

-- sources.origin_page_id / traces.skeleton_page_id 是可空的 FK 列，不是独立表，
-- 一并清空避免悬挂引用。
UPDATE sources SET origin_page_id = NULL WHERE origin_page_id IS NOT NULL;
UPDATE traces SET skeleton_page_id = NULL WHERE skeleton_page_id IS NOT NULL;

DELETE FROM wiki_pages;
