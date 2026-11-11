-- +goose Up
-- +sforum OnlineSafe
-- 徽章权限键落库。
--
-- 为什么需要这条迁移：app/Models/Identity/seeds.go 里的 SeedPermissions 只被
-- 测试与 CLI 读取，启动期没有任何权限目录 upsert。所以改了 seeds.go 之后，
-- 数据库里 permissions / role_permissions 依然是空的，角色挂不上 badge.*。
-- 权限目录的真值来源是迁移，seeds.go 必须与它逐字一致（测试会核对）。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO permissions (key, module, description) VALUES
  ('badge.view', 'forum', 'View badges and badge grants.'),
  ('badge.manage', 'forum', 'Create, update, disable, and delete badges; grant and revoke manual badges; recompute top badges.')
ON CONFLICT (key) DO NOTHING;

-- 只读：版主能看徽章目录与授予记录，不能改规则或手动授予。
INSERT INTO role_permissions (role_id, permission_key)
SELECT roles.id, 'badge.view'
FROM roles
WHERE roles.key IN ('moderator', 'operator', 'super_admin')
ON CONFLICT DO NOTHING;

-- 可写：站点运营可配置规则并手动授予。
INSERT INTO role_permissions (role_id, permission_key)
SELECT roles.id, 'badge.manage'
FROM roles
WHERE roles.key IN ('operator', 'super_admin')
ON CONFLICT DO NOTHING;

-- +goose Down
-- 只拆这条迁移自己加的两把钥匙：不触碰运营者后来手动授予的其它角色。
DELETE FROM role_permissions
WHERE permission_key IN ('badge.view', 'badge.manage');

DELETE FROM permissions
WHERE key IN ('badge.view', 'badge.manage');
