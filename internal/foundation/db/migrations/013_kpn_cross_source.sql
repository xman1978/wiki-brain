-- Migration 013: cross-source KPN (docs/impl/v1/kpn.md)

ALTER TABLE knowledge_point_relations ADD COLUMN scope TEXT NOT NULL DEFAULT 'intra';
-- intra（单 Source 内，存量默认）/ cross（跨 Source）

-- De-dup existing rows before adding the unique index below: keep the
-- earliest-created row per (source_point_id, target_point_id, relation_type).
DELETE FROM knowledge_point_relations
WHERE relation_id NOT IN (
    SELECT relation_id FROM (
        SELECT relation_id, ROW_NUMBER() OVER (
            PARTITION BY source_point_id, target_point_id, relation_type
            ORDER BY created_at ASC, relation_id ASC
        ) AS rn
        FROM knowledge_point_relations
    ) WHERE rn = 1
);

CREATE UNIQUE INDEX idx_kp_relations_uniq
  ON knowledge_point_relations(source_point_id, target_point_id, relation_type);
-- 防重复写入（跨批次/重复触发时 INSERT OR IGNORE 命中即跳过）
