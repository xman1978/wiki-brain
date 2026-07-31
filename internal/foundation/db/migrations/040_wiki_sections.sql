-- Wiki concept-page compilation: summary + aspect metadata columns.
-- See docs/impl/v1/wiki-generation.md 6.2/6.4.

-- lead/summary, matching the "## 摘要" section body — listed separately so
-- second-order (topic page) compilation can consume just this column
-- instead of full member page bodies.
ALTER TABLE wiki_pages ADD COLUMN summary TEXT NOT NULL DEFAULT '';

-- [{ aspect_id, heading, point_ids, question_types }] — same shape family as
-- topic pages' member_roles (member_roles describes what a topic page's
-- member pages carry, aspects describes what a concept page's "展开说明"
-- subsections carry). Metadata only — there is no per-aspect body table;
-- the compiled Markdown is written in one piece (docs/impl/v1/
-- wiki-generation.md 第 8 节: 不做按节持久化/增量重编译).
ALTER TABLE wiki_pages ADD COLUMN aspects TEXT NOT NULL DEFAULT '[]';
