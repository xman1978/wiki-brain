-- Migration 069: traces.source_affinity_source_ids — persists the source_ids
-- a confident full-path trace's recordSourceAffinity bound to its subject
-- (internal/trace/service.go), so that a later user feedback submission
-- (POST /traces/{id}/feedback) can find them without re-deriving from the
-- long-gone AnswerResult/EvidenceSet. 会话讨论 2026-08-26：负面反馈是
-- source_affinity 淘汰机制的第二个输入（第一个是捷径命中后证据不充分，见
-- migration 068）——客户明确给出负面反馈时记一次熔断失败，无反馈则忽略
-- （既不算成功也不算失败）。

ALTER TABLE traces ADD COLUMN source_affinity_source_ids TEXT;
