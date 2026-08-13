-- Migration 049: 问题四元组归一化存储（question_tuple_norms）
-- docs/impl/v1/retrieval.md 步骤 2「四元组归一化」：ActivationLink/
-- ActivationBundle/Wiki 四元组直答三处消费入口都已改为全字段精确匹配
-- （2026-08-12 定案），但 LLM 抽取存在措辞抖动，直接精确匹配对重复提问的
-- 命中率不稳定。本表是喂给这三处匹配器之前的归一化层：同一意思的问题第
-- 二次问出来时，把新抽取的四元组替换成第一次已经落库的"canonical"四元组
-- 再送去匹配，三处消费入口本身不改动。
--
-- 表按 domain_id 分域存储（一个问题可能有多个 domain_id，命中检查逐个域
-- 匹配，首次落库时按域各插入一行，域间不做去重）。

CREATE TABLE question_tuple_norms (
    norm_id         TEXT PRIMARY KEY,
    domain_id       TEXT NOT NULL,
    subject         TEXT NOT NULL,
    intent          TEXT NOT NULL,
    audience        TEXT NOT NULL,
    constraint_text TEXT NOT NULL,
    vector          TEXT,               -- JSON 编码的 float 数组（[0.1,0.2,...]），未启用向量匹配时为 NULL
    last_hit_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_qtn_domain ON question_tuple_norms(domain_id);
CREATE INDEX idx_qtn_domain_exact ON question_tuple_norms(domain_id, subject, intent, audience, constraint_text);
