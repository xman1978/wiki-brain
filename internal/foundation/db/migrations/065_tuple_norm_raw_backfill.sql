-- Migration 065: 回填 question_tuple_norms 存量行的 intent_raw/constraint_raw
-- migration 064 新增这两列时未回填存量行（默认空串）。这里补上，但有一个
-- 无法绕开的限制：原始短语（text.Normalize 输出，分词排序前）从未落库，
-- intent/constraint_text 两列存的是 text.Terms 处理后的结果（gse 分词+
-- 停用词过滤+字母序排序+空格拼接），这个过程本身丢信息（词序、原始空白），
-- 无法从词袋反推回原始短语。
--
-- 因此这里是"尽力而为"的回填：用词袋列本身填充 raw 列，而不是留空——
-- 空字符串会让 Tier 3 LLM 看到一个"什么都没有"的候选，比看到词袋（虽然
-- 丢了词序）更糟；这些存量行会在下次被同一四元组重新命中/插入时随查询侧
-- 新鲜的 Normalize 输出自然刷新为真正的原始短语，见 docs/impl/v1/
-- retrieval.md 步骤 2「2026-08-24 改判」。

UPDATE question_tuple_norms
SET intent_raw = intent
WHERE intent_raw = '';

UPDATE question_tuple_norms
SET constraint_raw = constraint_text
WHERE constraint_raw = '';
