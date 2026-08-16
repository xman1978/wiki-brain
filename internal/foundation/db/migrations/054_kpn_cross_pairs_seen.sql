-- Migration 054: 跨 Source KPN 已比对配对记账（修复幂等性问题的根因二）
-- CrossSourceKPN 每次触发都会把整个 entry_id 分组的候选（受
-- crossBatchMaxSize 限制、按 question_kp_cooccurrence.confident_count 截取）
-- 重新送去问模型，而这个截取信号会随真实使用持续变化，导致不同轮次触发问到
-- 的候选子集不同、持续发现"新"关系，看起来永不收敛。这张表记录每一对
-- (new_point_id, opposite_point_id) 是否已经被送进过 crossKPNBatch 的一次
-- LLM 调用（不论是否产出关系），后续触发时把已问过的配对从候选里排除，只送
-- 真正新出现的配对，使重复触发趋向收敛。

CREATE TABLE IF NOT EXISTS kpn_cross_pairs_seen (
    new_point_id      TEXT NOT NULL,
    opposite_point_id TEXT NOT NULL,
    seen_at           DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (new_point_id, opposite_point_id)
);

CREATE INDEX IF NOT EXISTS idx_kpn_cross_pairs_seen_opposite ON kpn_cross_pairs_seen(opposite_point_id);
