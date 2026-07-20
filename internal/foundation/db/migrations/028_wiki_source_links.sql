-- Migration 028: wiki_pages 补记编译依赖的 ActivationLink（docs/impl/v1/wiki.md 步骤 3 扩展）
-- 与 source_point_ids / source_unit_ids 同类的依赖回链字段：记录编译时
-- 已引用 KP 上存在的 verified ActivationLink，供页面详情展示和后续生命周期
-- 追溯使用，不参与编译输入本身。

ALTER TABLE wiki_pages ADD COLUMN source_link_ids TEXT NOT NULL DEFAULT '[]';
