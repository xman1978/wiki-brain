-- Migration 047: known-question-terms shortcut on activation_links
-- 记录该链接曾经被哪些字面问题（question_terms 归一化词集）成功命中过。
-- Match() 优先按问题字面匹配，命中直接激活；四元组抖动（同一问题不同轮次抽取出
-- 不同的 intent/audience/constraint 词集）只有在问题字面从未匹配过时才会走到，
-- 不再让同一句问法反复因为四元组不同而分裂成多个 observed_conditions 分组、
-- 反复退回慢路径（2026-08-09 决策，见对话记录）。

ALTER TABLE activation_links ADD COLUMN known_question_terms TEXT NOT NULL DEFAULT '[]';
