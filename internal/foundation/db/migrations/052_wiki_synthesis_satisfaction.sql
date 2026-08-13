-- Migration 052: Wiki 综合满意度轴（synthesis satisfaction）
-- (docs/design/activation-convergence.md 第 7 节, docs/impl/v1/wiki.md
-- 步骤 4a)。四个简单的可加计数列，不改写既有数据；存量行全部从
-- 0/0/0/0 起步，mean(page)=0.5（与 activation_links 观测条件新建时的
-- 起点公式一致，不预设"新写的页面默认可信"或"默认不可信"）。

ALTER TABLE wiki_pages ADD COLUMN synthesis_success_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_failure_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_audited_success_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE wiki_pages ADD COLUMN synthesis_audited_failure_count INTEGER NOT NULL DEFAULT 0;
