-- Migration 044: entries.boundary / entries.aliases
--
-- preset/domains.json 里每个预制词条本来就写了 boundary（边界说明）和
-- aliases（别名），但此前 boundary 从未被解析进 Go 结构体、从未落库；
-- aliases 只被喂进 subject_synonyms（Activation 阶段的主语近义词典），
-- 从未进入 unit_entry_match / kpn_entry_propose 能看到的上下文。这两处
-- 恰恰是 KU→预制词条匹配、新词条抽象层级参照最需要消歧信息的地方。
-- 新增两列承接这两块既有数据，供上述两处 Prompt 上下文渲染读取。
ALTER TABLE entries ADD COLUMN boundary TEXT;
ALTER TABLE entries ADD COLUMN aliases TEXT NOT NULL DEFAULT '[]';
-- aliases 为 JSON 字符串数组；存量行（含 evolved/content_driven 产生的
-- 概念）默认空数组，不回填猜测。
