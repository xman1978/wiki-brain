-- Migration 015: Concept evolution (docs/impl/v1/concept-evolution.md)

ALTER TABLE concepts ADD COLUMN merged_into TEXT REFERENCES concepts(concept_id);
-- 非空 = 已被合并，不再是当前认知入口；保留行用于追溯
ALTER TABLE concepts ADD COLUMN origin TEXT NOT NULL DEFAULT 'preset';
-- preset / evolved（人工确认新增的概念）

CREATE TABLE concept_candidates (
    candidate_id   TEXT PRIMARY KEY,
    kind           TEXT NOT NULL,
    -- add / merge（split 预留枚举值，V2 启用）
    domain_id      TEXT REFERENCES domains(domain_id),
    -- kind=add：建议归属领域（可为 NULL，确认时人工指定）
    suggested_name TEXT,
    -- kind=add：程序从簇内 KU center 高频词提取的建议名，确认时可改
    merge_from     TEXT NOT NULL DEFAULT '[]',
    -- kind=merge：JSON 数组，涉及的两个 concept_id
    point_ids      TEXT NOT NULL DEFAULT '[]',
    -- 关联 KnowledgePoint 集合（JSON 数组）
    evidence       TEXT NOT NULL DEFAULT '{}',
    -- 统计依据 JSON：事件数、不同问题数、KP 重叠度、共同采用次数等
    event_ids      TEXT NOT NULL DEFAULT '[]',
    -- 支撑候选的 learning_event event_id 列表（JSON 数组）
    status         TEXT NOT NULL DEFAULT 'pending_confirm',
    -- pending_confirm / applied / rejected / expired
    last_signal_at DATETIME NOT NULL,
    created_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_cc_status ON concept_candidates(status);
CREATE INDEX idx_cc_kind ON concept_candidates(kind);
