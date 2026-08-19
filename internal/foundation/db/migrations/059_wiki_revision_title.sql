-- Migration 059: wiki_revisions.title
--
-- 写作草稿(wiki_drafts)和修订记录(wiki_revisions)合并为一份统一的修订
-- 记录列表（用户 2026-08-19 要求：草稿保存就是一次新修订，二者本该是同一
-- 件事）。列表要展示"标题名"，但标题会随每次草稿保存变化，历史修订各自
-- 记录当时的标题才准确，不能只读页面当前标题。

ALTER TABLE wiki_revisions ADD COLUMN title TEXT NOT NULL DEFAULT '';
