-- Migration 050: ActivationLink 从离散状态机迁移到连续置信度
-- (docs/design/activation-convergence.md, docs/impl/v1/activation.md 状态机)。
--
-- observed_conditions 内每个元素：
--   hit_count            → success_count（原样承接，数值不变）
--   failure_count         = 0（新字段，存量数据从未按条件粒度记录失败，
--                             接受"此前从未失败过"的乐观先验，见文档）
--   audited_success_count = 0
--   audited_failure_count = 0
--   known_question_terms  = 迁移前 activation_links.known_question_terms
--                            （表级列）的值，原样复制进该链接名下每一条条件
--                            （2026-08-13 下沉，粗粒度近似，见文档「已知的
--                            数据缺口」——不做精确条件归属回填）
--
-- 随后 DROP 掉已下沉、不再被任何代码路径读取的表级 known_question_terms 列。

UPDATE activation_links
SET observed_conditions = (
	SELECT COALESCE(json_group_array(
		json_set(
			json_remove(je.value, '$.hit_count'),
			'$.success_count', json_extract(je.value, '$.hit_count'),
			'$.failure_count', 0,
			'$.audited_success_count', 0,
			'$.audited_failure_count', 0,
			'$.known_question_terms', json(activation_links.known_question_terms)
		)
	), '[]')
	FROM json_each(activation_links.observed_conditions) AS je
)
WHERE observed_conditions IS NOT NULL AND observed_conditions != '[]';

ALTER TABLE activation_links DROP COLUMN known_question_terms;

-- ActivationBundle（熟路，migration 048）的 observed_conditions 复用同一个
-- Go ObservedCondition 结构（hit_count 字段），同样需要改名/补字段；bundles
-- 没有表级 known_question_terms 列，known_question_terms 留空即可（阶段 1
-- 从未写过这个字段）。
UPDATE activation_bundles
SET observed_conditions = (
	SELECT COALESCE(json_group_array(
		json_set(
			json_remove(je.value, '$.hit_count'),
			'$.success_count', json_extract(je.value, '$.hit_count'),
			'$.failure_count', 0,
			'$.audited_success_count', 0,
			'$.audited_failure_count', 0
		)
	), '[]')
	FROM json_each(activation_bundles.observed_conditions) AS je
)
WHERE observed_conditions IS NOT NULL AND observed_conditions != '[]';
