-- Migration 074: system_settings — 文件转换服务、历史会话两组配置从
-- config.yml 迁移到数据库，页面即时编辑生效（比照 llm_providers/
-- llm_purpose_bindings 的做法）。key-value + JSON value，便于以后再挂新的
-- 配置分组而不用每次都加表。

CREATE TABLE system_settings (
    key        TEXT PRIMARY KEY,   -- 'fileview' | 'session'
    value      TEXT NOT NULL,      -- JSON
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);
