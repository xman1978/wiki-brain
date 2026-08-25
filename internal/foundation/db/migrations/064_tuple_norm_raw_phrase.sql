-- Migration 064: question_tuple_norms 新增 intent_raw/constraint_raw
-- docs/impl/v1/retrieval.md 步骤 2「问题四元组归一化」2026-08-24 改判：
-- Tier 3 LLM 判断改用未分词的原始短语，避免 intent/constraint 经
-- text.Terms（gse 分词+排序）后丢失词序/上下文信息；Tier 1 精确匹配、
-- Tier 2 Jaccard 继续用现有的 intent/constraint_text 词袋列，不受影响。
-- subject/audience 本身未分词（Normalize/NormalizeCompact），不新增列。

ALTER TABLE question_tuple_norms ADD COLUMN intent_raw TEXT NOT NULL DEFAULT '';
ALTER TABLE question_tuple_norms ADD COLUMN constraint_raw TEXT NOT NULL DEFAULT '';
