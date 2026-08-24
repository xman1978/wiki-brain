-- Migration 063: top-N / 目录检索系数自收敛的校准样本表
--
-- 设计依据 docs/design/topn-coefficient-convergence.md，实现依据
-- docs/impl/v1/topn-coefficient-convergence.md。记录慢路径每条 trace 在
-- 证据充分性判断（VerifyEvidenceSufficient）+ 候选池扩展重试链路上落入的
-- 五类结果之一，供 Study 离线计算 top-N/目录检索系数的建议值——本阶段只
-- 计算并展示建议值，不接入自动调整。

CREATE TABLE topn_calibration_samples (
    sample_id                 TEXT PRIMARY KEY,
    trace_id                  TEXT NOT NULL,
    created_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    n_at_query_time           INTEGER NOT NULL,
    coefficient_at_query_time REAL NOT NULL,
    -- tight / content_rescued / pool_rescued / pool_exhausted_before_2n / gap_at_2n
    completeness_class        TEXT NOT NULL,
    -- tight: 精确保守代理（被引用证据的最差 mergedRank）；
    -- pool_rescued: 区间上界（扩池后的 N，即 2 * n_at_query_time）；
    -- 其余三类不使用该字段，留 NULL。
    rank_proxy_lower          INTEGER,
    rank_proxy_is_interval    INTEGER NOT NULL DEFAULT 0,
    candidate_pool_size       INTEGER NOT NULL,
    -- 仅 pool_rescued 类填充：候选池（截至扩池后的 N）快照的 JSON 数组，
    -- 每项 {unit_id, point_id, merged_rank, rank_by_path}，供系数网格重放。
    pool_snapshot_json        TEXT,
    -- 仅 pool_rescued 类填充：该 trace 实际引用的证据 unit_id 列表（JSON 数组）
    -- ——系数重放要检验的就是"这些 unit 在候选 c 下的新排名是否仍落在 N 内"。
    cited_unit_ids_json       TEXT
);

CREATE INDEX idx_topn_calib_created ON topn_calibration_samples(created_at);
CREATE INDEX idx_topn_calib_class ON topn_calibration_samples(completeness_class);

-- 防止同一条 trace 因为重跑 Study 扫描而被重复计入。
CREATE TABLE topn_calibration_event_dedup (
    trace_id      TEXT PRIMARY KEY,
    first_seen_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
