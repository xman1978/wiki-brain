-- Migration 051: ActivationBundle 成员轴从静态核心/路肩标签迁移到连续置信度
-- (docs/design/activation-convergence.md 第 6 节, docs/impl/v1/
-- activation-bundle.md「成员置信度：Bundle 独有的第二根轴」)。
--
-- member_point_ids 从裸字符串数组变成对象数组：
--   [{"point_id","success_count","failure_count","last_seen_at"}]
-- 种子值（占位，不是真实回填——下一轮显影扫描会用真实数据覆盖）：
--   success_count = 1
--   failure_count = 0
--   last_seen_at  = 该 bundle 自己的 updated_at
--
-- fringe_point_ids 随之整体废弃：路肩不再是独立存储的静态标签，是
-- member_point_ids 里同一批成员当前 mean/tier 落在低档区间的那部分，
-- 一次 SELECT 按 tier 过滤即可，不需要维护两份平行存储。

UPDATE activation_bundles
SET member_point_ids = (
	SELECT COALESCE(json_group_array(
		json_object(
			'point_id', je.value,
			'success_count', 1,
			'failure_count', 0,
			'last_seen_at', activation_bundles.updated_at
		)
	), '[]')
	FROM json_each(activation_bundles.member_point_ids) AS je
)
WHERE member_point_ids IS NOT NULL AND member_point_ids != '[]';

ALTER TABLE activation_bundles DROP COLUMN fringe_point_ids;
