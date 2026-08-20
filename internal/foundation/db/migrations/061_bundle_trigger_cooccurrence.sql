-- Migration 061: Bundle 触发轴的持久化聚合表（2026-08-20 生成重设计）
--
-- ActivationBundle 的身份从"四元组文本聚类簇"改为"归一化四元组"
-- （docs/design/activation-bundle.md 改判，docs/impl/v1/activation-bundle.md
-- 步骤 4）。这张表跟 question_kp_cooccurrence 是同一种簿记，只是键从
-- point_id 换成归一化四元组：一条 confident 且 direct_point_ids ≥2 的
-- trace，归一化后按 (domain_id, subject, intent, audience, constraint_text)
-- 累加 hit_count/confident_count，Study 每轮用同一套 Beta 均值/宽度公式
-- （study.create_confidence_min/create_width_max）判断是否越过创建门槛。

CREATE TABLE bundle_trigger_cooccurrence (
    trigger_id      TEXT PRIMARY KEY,
    domain_id       TEXT NOT NULL,
    subject         TEXT NOT NULL,
    intent          TEXT NOT NULL,
    audience        TEXT NOT NULL,
    constraint_text TEXT NOT NULL,
    hit_count       INTEGER NOT NULL DEFAULT 0,
    confident_count INTEGER NOT NULL DEFAULT 0,
    last_seen_at    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(domain_id, subject, intent, audience, constraint_text)
);

CREATE INDEX idx_bundle_trigger_cooc_domain ON bundle_trigger_cooccurrence(domain_id);

-- 镜像 cooccurrence_question_dedup 的角色：防止同一条 trace 在 Study 每轮
-- 扫描时被重复计入 bundle_trigger_cooccurrence。
CREATE TABLE cooccurrence_bundle_dedup (
    trace_id      TEXT PRIMARY KEY,
    first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
