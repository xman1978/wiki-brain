-- Migration 024: rerank 语义人工修正（docs/impl/v1/semantics-curation.md）。
-- manually_edited=1 的行经人工修正，自动重抽取/回填必须跳过（--force-manual
-- 显式覆盖除外）；edited_at 记录最近一次人工修正时间。

ALTER TABLE unit_rerank_semantics ADD COLUMN manually_edited INTEGER NOT NULL DEFAULT 0;
ALTER TABLE unit_rerank_semantics ADD COLUMN edited_at DATETIME;
