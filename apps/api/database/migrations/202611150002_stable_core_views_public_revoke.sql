-- +goose Up
-- +sforum OnlineSafe
-- sforum_core_v1 的「PUBLIC 零权限」从一次性动作变成可重复兑现的约定（EX2-07）。
--
-- 202607140016 建 schema 时写过一次
-- `REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC`，之后的 8 条迁移
-- 又往同一个 schema 里新增了视图，却没有再 REVOKE。新对象默认不授予 PUBLIC，
-- 所以现状并没有暴露；缺的是 fail-closed 的约定——一旦有人动了默认权限
-- （ALTER DEFAULT PRIVILEGES ... GRANT ... TO PUBLIC），新视图就会带着 PUBLIC
-- 可写面出厂，而没有任何地方会报错。
--
-- 本次只补 REVOKE，**不改任何历史迁移**：goose 按 version 记账、没有 checksum，
-- 改历史文件对已经升级过的库完全无效。新增视图的迁移必须在自己的 Up 段里再写一条
-- schema 级 REVOKE——静态棘轮会扫全部迁移（见
-- stable_core_views_public_revoke_test.go）。
--
-- OnlineSafe：REVOKE 只改对象 ACL 元数据，不重写表、不扫数据；锁是对象级且瞬时完成。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

REVOKE ALL ON ALL TABLES IN SCHEMA sforum_core_v1 FROM PUBLIC;
REVOKE ALL ON SCHEMA sforum_core_v1 FROM PUBLIC;

-- +goose Down
-- 只收紧权限、不改结构：Down 不恢复 PUBLIC 权限，回滚这条迁移不应重新打开已知风险面。
SELECT 1;
