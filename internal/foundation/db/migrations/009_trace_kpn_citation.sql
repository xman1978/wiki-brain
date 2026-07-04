-- Migration 009: KPN citation adoption stats on traces
-- Supports Study report summary.kpn_citation_rate: what share of Answer's
-- actually-cited evidence originated from KPN expansion (retrieval step 8)
-- rather than from Rerank-classified direct/supporting evidence.

ALTER TABLE traces ADD COLUMN kpn_cited_count INTEGER NOT NULL DEFAULT 0;
-- 引用的 fact_id 中，来源为 KPN 扩展（Evidence.origin = 'kpn_expansion'）的数量
ALTER TABLE traces ADD COLUMN cited_count INTEGER NOT NULL DEFAULT 0;
-- 引用的 fact_id 中，能在 evidence_snapshot 里解析出 origin 的总数（分母）
