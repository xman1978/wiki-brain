-- Migration 043: Concept kind classification (docs/impl/v1/kpn.md 步骤 3
-- "类型标注（kind：concept / fact，2026-08-04 新增，最小可行版本）")
--
-- entries.kind: concept（底层理论/原理/规则，跨具体实现成立）or fact（具体
-- 实现/技术/产品实例）。存量行统一置 concept，不做回填猜测.
ALTER TABLE entries ADD COLUMN kind TEXT NOT NULL DEFAULT 'concept';

-- entry_candidates already has a column named "kind" with an unrelated
-- meaning (add/merge/split — see 015_concept_evolution.sql), so the new
-- concept/fact classification is stored as entry_kind here to avoid a
-- naming collision, mirrored onto entries.kind at confirm time.
ALTER TABLE entry_candidates ADD COLUMN entry_kind TEXT NOT NULL DEFAULT 'concept';
