-- Migration 046: recall-path adoption stats on traces
-- 记录最终被 Answer 引用的证据里，来自目录结构召回（outline）的比例，以及这些
-- 证据在 RRF 合并列表（rerank_top_n 截断前）中的排名总和——用于事后分析
-- outline 召回是否确实比 FTS 更准（排名更靠前），为调整 rrf_merge 排序权重和
-- rerank_top_n 提供数据依据，而不是凭直觉调参。

ALTER TABLE traces ADD COLUMN outline_cited_count INTEGER NOT NULL DEFAULT 0;
-- 引用的 fact_id 中，来源候选的 sourcePaths 包含 "outline" 的数量
ALTER TABLE traces ADD COLUMN cited_rank_sum INTEGER NOT NULL DEFAULT 0;
-- 引用的 fact_id 对应候选在 RRF 合并列表中的排名（0-based）之和，
-- 配合已有 cited_count 作分母可得平均排名：cited_rank_sum / cited_count
