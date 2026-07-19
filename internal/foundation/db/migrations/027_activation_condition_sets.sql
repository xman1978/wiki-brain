-- Migration 027: ActivationLink 条件字段重构——intent/audience/constraint 从
-- 单值改为累积白名单集合（JSON 数组存于既有 TEXT 列），并把"每 point 至多一条
-- 链接"这个已经是代码现状（study.tryCreateLink 的去重检查）的不变量提升到
-- DB 级约束（docs/impl/v1/activation.md）。

-- 去重：历史重复（早于 2026-07-18 去重修订）各 point_id 只保留一条。部分重复对
-- created_at 完全相同（同一批次写入），仅按 created_at 取 MAX 无法唯一区分，
-- 用 rowid 兜底做稳定 tie-break，保证每个 point_id 恰好剩一行。
DELETE FROM activation_links
WHERE rowid NOT IN (
  SELECT rowid FROM (
    SELECT rowid, ROW_NUMBER() OVER (
      PARTITION BY point_id ORDER BY created_at DESC, rowid DESC
    ) AS rn
    FROM activation_links
  )
  WHERE rn = 1
);

DROP INDEX IF EXISTS idx_al_point_id;
CREATE UNIQUE INDEX idx_al_point_id ON activation_links(point_id);

-- 单值 -> 单元素 JSON 数组，空值 -> 空数组；json_array() 自带转义，比手拼字符串安全
UPDATE activation_links SET intent_terms     = CASE WHEN intent_terms = ''     THEN '[]' ELSE json_array(intent_terms)     END;
UPDATE activation_links SET audience         = CASE WHEN audience = ''         THEN '[]' ELSE json_array(audience)         END;
UPDATE activation_links SET constraint_terms = CASE WHEN constraint_terms = '' THEN '[]' ELSE json_array(constraint_terms) END;
