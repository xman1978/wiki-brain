-- Migration 067: 单条 trace 的四元组归一化结果缓存（Study 增量化）
--
-- 根因（2026-08-25 实测定位）：buildObservedConditions（ActivationLink）与
-- buildBundleObservedConditionsAndMembers（ActivationBundle）都是"每次全量
-- 重算，不做增量缓存"——每轮 Study 运行都要把该 point/该 Bundle 候选涉及的
-- 全部历史 confident trace 重新跑一遍 activation.TupleNormalizer.Normalize。
-- 单条 trace 的 (subject, intent, audience, constraint) 原文本身从写入起
-- 就不会再变，NormalizeTuple 的结果在 Tier1/2/3 判定口径不变的前提下对同一
-- 条 trace 是确定性的——真正需要重新判定的只有"这条 trace 之前从未被归一化
-- 过"，而不是"每一轮都要对所有历史 trace 重新问一遍"。
--
-- 本表按 trace_id 缓存每条 trace 第一次被归一化时的结果，命中 Bundle 成员
-- 名单重建 / ActivationLink observed_conditions 重建时直接查表，不再重新
--调用 NormalizeTuple（也就不再重新发起 Tier3 LLM 请求）。首次出现的 trace
-- 仍然要走一次完整的 Tier1/2/3 判定并写入本表，之后永久复用。
CREATE TABLE trace_tuple_norm_cache (
    trace_id        TEXT PRIMARY KEY REFERENCES traces(trace_id),
    subject         TEXT NOT NULL,
    intent          TEXT NOT NULL,
    audience        TEXT NOT NULL,
    constraint_text TEXT NOT NULL,
    intent_raw      TEXT NOT NULL DEFAULT '',
    constraint_raw  TEXT NOT NULL DEFAULT '',
    computed_at     DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 按归一化四元组查成员名单（buildBundleObservedConditionsAndMembers 的核心
-- 查询路径）需要按这四个字段做等值过滤。
CREATE INDEX idx_trace_tuple_norm_cache_tuple
    ON trace_tuple_norm_cache(subject, intent, audience, constraint_text);
