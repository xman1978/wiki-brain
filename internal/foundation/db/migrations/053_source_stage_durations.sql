-- Migration 053: Source 处理阶段耗时（stage duration）
-- 文件管理页时间线原先展示各阶段的绝对开始时间，改为展示该阶段自身耗时
-- （单位 ms）。原有 processing_started_at/completed_at/units_built_at/
-- units_completed_at 等绝对时间戳列保留不动（状态判断等逻辑仍依赖它们），
-- 这里新增四个耗时列，由写入对应阶段完成时间的同一条 UPDATE 顺带计算写入。
-- 存量已上传文件的处理阶段一并从已有时间戳回填耗时。

ALTER TABLE sources ADD COLUMN register_duration_ms INTEGER;
ALTER TABLE sources ADD COLUMN convert_duration_ms INTEGER;
ALTER TABLE sources ADD COLUMN units_duration_ms INTEGER;
ALTER TABLE sources ADD COLUMN semantics_duration_ms INTEGER;

UPDATE sources SET register_duration_ms = CAST((julianday(processing_started_at) - julianday(created_at)) * 86400000 AS INTEGER)
	WHERE processing_started_at IS NOT NULL;

UPDATE sources SET convert_duration_ms = CAST((julianday(completed_at) - julianday(processing_started_at)) * 86400000 AS INTEGER)
	WHERE completed_at IS NOT NULL AND processing_started_at IS NOT NULL;

UPDATE sources SET units_duration_ms = CAST((julianday(units_built_at) - julianday(completed_at)) * 86400000 AS INTEGER)
	WHERE units_built_at IS NOT NULL AND completed_at IS NOT NULL;

UPDATE sources SET semantics_duration_ms = CAST((julianday(units_completed_at) - julianday(units_built_at)) * 86400000 AS INTEGER)
	WHERE units_completed_at IS NOT NULL AND units_built_at IS NOT NULL;
