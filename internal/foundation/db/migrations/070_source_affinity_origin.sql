-- Migration 070: source_affinity 增加 origin 列，区分绑定的可信来源。
--
-- 设计依据：会话讨论 2026-08-27（docs/design/retrieval.md 第 14 节、
-- docs/impl/v1/retrieval.md 步骤 5a）。migration 068 的绑定只有一种来源——
-- Trace 确认慢路径答案 confident 且真被引用（origin='citation'，本次迁移
-- 前的全部存量行都算这一种，DEFAULT 即体现这一点）。新增的后台主动打标签
-- （用一份 source 的标题/摘要去匹配 domain 下已有的主题标签库）产生的是
-- 语义匹配级别的证据（跟 sourceSemanticFilter 单独一次判断同一个量级的可信
-- 度），不应该被 trySourceAffinityShortcut 当成和引用驱动的绑定同样可信：
-- 引用驱动的绑定命中时跳过 domain 预过滤 + source 语义过滤两次 LLM 调用没
-- 问题（已经被真实回答验证过）；纯语义匹配打的标签命中时，域预过滤可以跳
-- （标签本身已经域内），但 source 语义过滤这一步不该跳，否则相当于让"标题
-- 摘要匹配"绕过了它自己的验证环节直接生效。
--
-- origin='citation' 优先于 'backfill'：真实引用发生时永远把标签升级为
-- citation（RecordSourceAffinitySuccess 的 upsert 补充 origin 赋值），不会
-- 反向降级；backfill 来源的写入用 INSERT ... ON CONFLICT DO NOTHING，不覆盖
-- 已有行。

ALTER TABLE source_affinity ADD COLUMN origin TEXT NOT NULL DEFAULT 'citation';
