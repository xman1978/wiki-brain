-- Migration 068: 主题 → source 绑定（source_affinity），慢路径的 domain
-- 预过滤 + source 语义过滤（LLM 调用）跳过捷径。
--
-- 设计依据：会话讨论 2026-08-25。主题和 source 的对应关系比 ActivationLink
-- 绑定的四元组→KP 关系更稳定（不需要 intent/audience/constraint 参与判断，
-- 只看"这个主题该去哪个文件找"，颗粒度粗一级），所以不套用 ActivationLink
-- 的 Beta 后验置信度机制：绑定首次由 Trace 确认命中即建立，不设生成门槛；
-- 服务时命中就直接用，不做 exploring/trusted 概率抽样；纠错只用一个简单的
-- 连续失败计数器（熔断），达到阈值直接删除绑定，下次再有确认命中会重新
-- 建立——比 activation_links 的机制轻一个数量级，因为绑定错了的代价也低
-- 得多（只是多走一次完整慢路径重试，不会像 ActivationLink 直接把错误证据
-- 送给用户）。
--
-- subject_norms 是独立于 question_tuple_norms 的主题专用归一化表：
-- question_tuple_norms 的 Tier1 精确匹配要求 subject/intent/audience/
-- constraint 四项同时对齐才算命中，同一个真实主题配上不同 intent 时四元组
--整体不命中，没法覆盖"同一份文件回答很多不同问题"这种场景，所以不能直接
-- 复用那张表，需要一张只认 subject 一个字段的归一化表，按 domain 分域。

CREATE TABLE subject_norms (
    norm_id     TEXT PRIMARY KEY,
    domain_id   TEXT NOT NULL,
    subject     TEXT NOT NULL,
    last_hit_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_subject_norms_domain ON subject_norms(domain_id);
CREATE INDEX idx_subject_norms_domain_exact ON subject_norms(domain_id, subject);

-- 一个 (domain_id, subject_norm) 可以绑定多个 source_id（并集限定慢路径候选
-- 范围）。consecutive_failures 是上面说的熔断计数器：命中后证据不充分即 +1，
-- 命中且被 Trace 确认引用即清零；达到 source_affinity_failure_max（配置项）
-- 时整行删除。
CREATE TABLE source_affinity (
    affinity_id          TEXT PRIMARY KEY,
    domain_id            TEXT NOT NULL,
    subject_norm         TEXT NOT NULL,
    source_id            TEXT NOT NULL,
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    created_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at           TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(domain_id, subject_norm, source_id)
);

CREATE INDEX idx_source_affinity_lookup ON source_affinity(domain_id, subject_norm);
