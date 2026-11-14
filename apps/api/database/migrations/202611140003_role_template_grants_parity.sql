-- +goose Up
-- +sforum OnlineSafe
-- 内置角色模板权限包收敛（PRD2-04）。
--
-- 权威源是 app/Models/Identity/seeds.go 的 SeedRoleTemplates；权限目录的真值落在
-- 迁移 SQL 里（启动期没有 upsert），所以两边历史漂移必须在这里收敛。
-- 收敛前的差集只有 operator 两处：
--   * DB 有 trust.manage（202611100004 写入），seeds 之前漏写 → 已在 seeds 补齐；
--   * seeds 有 entity_meta.manage，DB 只授给了 super_admin（202607120009）→ 本迁移补齐。
-- attachment.upload_policy.manage（202607310002）与 ai.manage（202610020002）更早的迁移
-- 已经授给了对应岗位；这里仍显式重复声明，让「全新库按任意顺序跑」都能得到同一结果。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO role_permissions (role_id, permission_key)
SELECT roles.id, grants.permission_key
FROM roles
CROSS JOIN (VALUES
  ('operator', 'entity_meta.manage'),
  ('operator', 'trust.manage'),
  ('operator', 'attachment.upload_policy.manage'),
  ('tech_admin', 'ai.manage')
) AS grants(role_key, permission_key)
WHERE roles.key = grants.role_key
ON CONFLICT DO NOTHING;

-- +goose Down
-- 只拆本迁移**唯一来源**的那一条：operator × entity_meta.manage。
--
-- 另外三条不是本迁移拥有的：trust.manage 属于 202611100004，attachment.upload_policy.manage
-- 属于 202607310002，ai.manage 属于 202610020002。回滚时删它们会连带撤掉别的迁移的授权，
-- 也会破坏「seeds 里的键必须在库里存在」这条更早建立的不变量
-- （database/migrations 的 TestEverySeededPermissionReachesTheDatabase）。
DELETE FROM role_permissions
WHERE permission_key = 'entity_meta.manage'
  AND role_id IN (SELECT id FROM roles WHERE key = 'operator');
