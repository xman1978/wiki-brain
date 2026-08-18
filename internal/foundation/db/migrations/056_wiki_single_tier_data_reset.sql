-- Migration 056: Wiki 单层化改造 -- 存量数据清理
-- docs/design/wiki-single-tier-revision.md / docs/impl/v1/wiki-single-tier-task-brief.md 步骤 0
--
-- 两层架构（概念页 + 主题页）与四元组直答匹配的存量已发布数据一律不迁移，
-- 直接清空。只删数据，不删表结构——新的单层编译逻辑仍复用这些表。
-- 按外键依赖顺序：先清引用 wiki_pages.page_id 的子表，最后清 wiki_pages 本身。

DELETE FROM wiki_revisions;
DELETE FROM wiki_page_relations;
DELETE FROM wiki_drafts;
DELETE FROM wiki_claim_checks;
DELETE FROM wiki_quality_checks;

-- sources.origin_page_id / traces.skeleton_page_id 是可空的 FK 列，不是独立表，
-- 一并清空避免悬挂引用。
UPDATE sources SET origin_page_id = NULL WHERE origin_page_id IS NOT NULL;
UPDATE traces SET skeleton_page_id = NULL WHERE skeleton_page_id IS NOT NULL;

DELETE FROM wiki_pages;
