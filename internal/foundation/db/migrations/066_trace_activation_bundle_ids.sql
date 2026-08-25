-- ActivationBundle 命中的问法列表此前只查 activation_bundles.created_from（创建时来源），
-- 没有对称于 activation_link_ids 的"匹配命中"记录，导致详情页"命中问法"永远为空，
-- 即便该 bundle 已经被匹配命中并统计了 adopt_count/fail_count。
ALTER TABLE traces ADD COLUMN activation_bundle_ids TEXT NOT NULL DEFAULT '[]';
