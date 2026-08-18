-- Migration 058: 删除"主题页骨架注入慢路径"死代码的存储侧残留
-- docs/impl/v1/wiki-single-tier-open-questions.md「已拍板（2026-08-18）」
--
-- 单层化改造后 wiki.gatherDirectAnswerCandidates 恒不产出 skeleton，
-- traces.skeleton_page_id 永远写 NULL；系统尚无正式用户，无需兼容存量数据，
-- 直接删列。learning_events.event_type='topic_decompose_signal' 复用的是
-- 通用 payload JSON 字段（learning_events 表本身没有专属列），因此该事件类型
-- 的历史行本次不做迁移，只是停止写入新行。

ALTER TABLE traces DROP COLUMN skeleton_page_id;
