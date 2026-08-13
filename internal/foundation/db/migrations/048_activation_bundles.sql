-- Migration 048: ActivationBundle（熟路）阶段 1 存储
-- docs/impl/v1/activation-bundle.md：多 KP 组合激活对象，锚点是归一化四元组
-- 聚类身份（经两级 Match 判断，非分组指纹相等），成员集合随使用漂移。
-- 阶段 1 只做存储 + 匹配 + Study 显影扫描 + 只读 API，不含 Retrieval 消费/
-- Trace 回写（阶段 2），因此没有 bundle_success/bundle_failure 事件驱动的窗口
-- 统计列——adopt_count/fail_count 在阶段 1 保持 0，status 停留在 candidate。

CREATE TABLE activation_bundles (
    bundle_id            TEXT PRIMARY KEY,
    cluster_fingerprint  TEXT NOT NULL,      -- 展示/调试书签，非去重键（去重靠 Match）
    representative_terms TEXT NOT NULL DEFAULT '',
    observed_conditions  TEXT NOT NULL DEFAULT '[]',
    member_point_ids     TEXT NOT NULL DEFAULT '[]', -- 核心成员
    fringe_point_ids     TEXT NOT NULL DEFAULT '[]', -- 路肩成员
    status               TEXT NOT NULL DEFAULT 'candidate',
    adopt_count           INTEGER NOT NULL DEFAULT 0,
    fail_count            INTEGER NOT NULL DEFAULT 0,
    last_used_at          DATETIME,
    created_from          TEXT NOT NULL DEFAULT '[]',
    status_changed_at     DATETIME,
    created_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_ab_cluster ON activation_bundles(cluster_fingerprint);
CREATE INDEX idx_ab_status  ON activation_bundles(status);
