-- Migration 025: knowledge_gaps gains a root-cause breakdown so Study reports
-- can tell "never retrieved" apart from "retrieved but judged irrelevant"
-- apart from "LLM generation failed" (docs/impl/v1/study.md
-- "knowledge_gaps 表扩展").

ALTER TABLE knowledge_gaps ADD COLUMN reason_counts TEXT NOT NULL DEFAULT '{}';
-- JSON object, e.g. {"no_candidates":5,"judge_filtered":2}
ALTER TABLE knowledge_gaps ADD COLUMN last_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE knowledge_gaps ADD COLUMN last_trace_id TEXT NOT NULL DEFAULT '';
