-- Migration 029: wiki_pages 增加检索触发字段（docs/impl/v1/wiki.md 数据结构，
-- Wiki 路由方案 2026-07-21 定案）。aliases / trigger_questions 由编译时 LLM
-- 生成，只进 wiki index 参与检索打分，不属于正文，不参与 citation 白名单。

ALTER TABLE wiki_pages ADD COLUMN aliases TEXT NOT NULL DEFAULT '[]';
ALTER TABLE wiki_pages ADD COLUMN trigger_questions TEXT NOT NULL DEFAULT '[]';
