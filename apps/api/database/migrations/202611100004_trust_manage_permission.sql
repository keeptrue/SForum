-- +goose Up
-- +sforum OnlineSafe
-- 信任等级阈值写权限落库（计划 T5）。
--
-- 为什么需要这条迁移：Identity/seeds.go 的 SeedPermissions 只被测试与 CLI 读取，
-- 启动期没有任何权限目录 upsert —— 权限目录的真值来源是迁移 SQL。
-- seeds.go 里已经写了 PermissionTrustManage = "trust.manage"，但库与前端都没有它，
-- 于是「改阈值」这件事无法授予任何人：端点永远 403，且运行时不会有任何报错，
-- 只有 TestEverySeededPermissionReachesTheDatabase 会红。
--
-- 刻意不建 trust.view：后台读页复用 admin.access（D9）。上一刀 badge.view 的教训是
-- 「只勾 view 不勾 admin.access 就是死权限」—— 可达性依赖别的键，等于没给。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO permissions (key, module, description) VALUES
  ('trust.manage', 'identity', 'Update trust level thresholds, per-level limits, and earned capability mappings.')
ON CONFLICT (key) DO NOTHING;

-- 与 badge.manage 同一批岗位：阈值是站点策略，站点运营可配，超管全能。
-- 版主（moderator）不拿：它只做内容审核，拿站点策略写权限属于越界。
INSERT INTO role_permissions (role_id, permission_key)
SELECT roles.id, 'trust.manage'
FROM roles
WHERE roles.key IN ('operator', 'super_admin')
ON CONFLICT DO NOTHING;

-- +goose Down
-- Down 会删除该键的全部角色授予（含迁移之后手动授予给其它角色的），因为权限行本身
-- 要被删除、残留会违反外键（role_permissions.permission_key → permissions.key）；
-- 其它权限键不受影响。
--
-- 因此这里**不是**「只拆自己加的那一把」：`WHERE permission_key = 'trust.manage'` 是按 key 全删。
-- （202611100003_badges_permissions.sql 的 Down 用了「只拆自己加的钥匙」措辞，但 SQL 同样是
--  按 key 全删；本文件按真实行为写注释，不沿用那句不一致的描述。）
DELETE FROM role_permissions WHERE permission_key = 'trust.manage';

DELETE FROM permissions WHERE key = 'trust.manage';
