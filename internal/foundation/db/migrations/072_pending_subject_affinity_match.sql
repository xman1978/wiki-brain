-- Migration 072: 待匹配主题队列（pending_subject_affinity_match）。
--
-- 设计依据：会话讨论 2026-08-27（docs/design/retrieval.md 第 14 节、
-- docs/impl/v1/retrieval.md 步骤 5a 的后续修订）。原来"引用驱动"的写入路径
-- （每次慢路径答案 confident 就直接把这次答案实际引用到的 source 绑定到
-- 归一化主题上）范围太窄，只记录了"这次问答恰好用到的 source"，不是"这个
-- 主题该对应哪些 source"的完整匹配。改为：每次慢路径问答把归一化后的主题
-- 入队，Study 周期性批量处理——把主题和该 domain 下全部 source 做一次完整
-- 的 sourceSemanticFilter 匹配，写成 source_affinity 标签。
--
-- 这不是 question_kp_cooccurrence/bundle_trigger_cooccurrence 那种"永久
-- 累积计数、每轮全表重新聚合"的信号表，是纯粹的一次性待办队列：处理完
-- 就删，不是持续累积的统计对象。(domain_id, subject_norm) 做主键天然去重
-- ——同一个主题在被处理之前不管入队多少次都只占一行。

CREATE TABLE pending_subject_affinity_match (
    domain_id    TEXT NOT NULL,
    subject_norm TEXT NOT NULL,
    queued_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (domain_id, subject_norm)
);
