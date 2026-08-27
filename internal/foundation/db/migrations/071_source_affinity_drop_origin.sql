-- Migration 071: 撤销 migration 070 的 origin 列与信任分级。
--
-- 设计依据：会话讨论 2026-08-27（当天晚些时候，取代 070 的设计）。用户明确
-- 否决了"后台主动打标签"产生的绑定需要在查询时额外跑一次 sourceSemanticFilter
-- 验证这件事：问题归一化主题和 source 主题标签的精确匹配本身就足以定位
-- source，不需要"是否被真实答案引用过"这层额外验证。既然不再区分可信度，
-- origin 列失去存在意义，直接删除，trySourceAffinityShortcut 回到 migration
-- 068 最初的行为——命中直接跳过 domain 预过滤 + source 语义过滤两次调用。

ALTER TABLE source_affinity DROP COLUMN origin;
