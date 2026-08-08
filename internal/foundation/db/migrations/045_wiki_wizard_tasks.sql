-- Migration 045: wiki_wizard_tasks
--
-- 手动生成 Wiki 分步向导第一步（候选检索：全文 + 目录 + LLM 相关性判定，
-- 耗时 30-60 秒）此前只存在于前端内存状态，刷新页面或误关弹窗即彻底
-- 丢失、且无处找回。这张表把该任务的状态落库，后台 goroutine 跑检索、
-- 前端轮询同一行；任务完成（草稿页建成）后直接删除该行，不设
-- completed 状态。domain_id UNIQUE 直接实现"同一领域同时只允许一个
-- 进行中的向导任务"，不需要额外的业务层判断或部分索引。
CREATE TABLE wiki_wizard_tasks (
    task_id               TEXT PRIMARY KEY,
    domain_id             TEXT NOT NULL UNIQUE REFERENCES domains(domain_id),
    topic_name            TEXT NOT NULL,
    topic_description     TEXT NOT NULL DEFAULT '',
    status                TEXT NOT NULL DEFAULT 'candidates_loading',
    candidates_json       TEXT NOT NULL DEFAULT '[]',
    selected_members_json TEXT NOT NULL DEFAULT '[]',
    error_message         TEXT,
    created_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at            DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
