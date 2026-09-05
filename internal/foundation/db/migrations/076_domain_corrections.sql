-- Migration 076: domain_corrections table — 域纠错学习闭环
-- (docs/impl/v1/retrieval.md Fallback 3, docs/impl/v1/study.md
-- "domain_corrections 表")，对称于 knowledge_gaps（迁移 006/025）。
-- 与 knowledge_gaps 不同：这里没有 reason_counts 式的累计直方图，每次命中
-- 只覆盖写最新的 attempted/resolved_domain_ids——保留的是"最近一次纠错结
-- 果"，不是"多种失败原因各出现几次"。

CREATE TABLE IF NOT EXISTS domain_corrections (
    correction_id        TEXT PRIMARY KEY,
    question_terms       TEXT NOT NULL,
    question             TEXT NOT NULL,
    attempted_domain_ids TEXT NOT NULL DEFAULT '[]', -- JSON array
    resolved_domain_ids  TEXT NOT NULL DEFAULT '[]', -- JSON array
    hit_count            INTEGER NOT NULL DEFAULT 1,
    last_trace_id        TEXT NOT NULL DEFAULT '',
    created_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(question_terms)
);
