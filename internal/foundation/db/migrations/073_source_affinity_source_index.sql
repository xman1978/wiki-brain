-- Migration 073: source_affinity 按 source_id 反查的索引。
--
-- 设计依据：会话讨论 2026-08-27（source 详情页新增"主题标签"管理面板）。
-- 现有唯一索引 idx_source_affinity_lookup 是 (domain_id, subject_norm)，
-- 服务查询路径（trySourceAffinityShortcut）按这个方向查；人工在 source
-- 详情页查看/管理"这个 source 身上挂了哪些标签"需要反方向按 source_id
-- 查，没有索引会是全表扫描。

CREATE INDEX idx_source_affinity_source ON source_affinity(source_id);
