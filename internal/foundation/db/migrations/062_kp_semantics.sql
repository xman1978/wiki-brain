-- Migration 062: KU 级 rerank 语义下沉为 KP 级（2026-08-21 改判）
--
-- unit_rerank_semantics（KU 级 source_theme/content_theme/intent/object/scope）
-- 天然无法反映同一 KU 内不同 KP 各自不同的对象/范围，导致 rerank 证据过滤
-- 借用整个 KU 的语义时把无关 KP 的措辞当成佐证放行。改为每条 KP 各自一份。

ALTER TABLE knowledge_points ADD COLUMN source_theme TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_points ADD COLUMN content_theme TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_points ADD COLUMN object TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_points ADD COLUMN scope TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_points ADD COLUMN semantics_prompt_version TEXT NOT NULL DEFAULT '';

DROP TABLE unit_rerank_semantics;
