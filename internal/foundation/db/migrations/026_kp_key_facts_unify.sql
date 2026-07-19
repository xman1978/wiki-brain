-- Migration 026: key_facts 废弃，KP（knowledge_points）成为 rerank 判定与
-- 人工修正的统一事实来源（docs/impl/v1/semantics-curation.md 重写版）。

ALTER TABLE unit_rerank_semantics DROP COLUMN key_facts_json;

ALTER TABLE knowledge_points ADD COLUMN manually_edited INTEGER NOT NULL DEFAULT 0;
ALTER TABLE knowledge_points ADD COLUMN edited_at DATETIME;
