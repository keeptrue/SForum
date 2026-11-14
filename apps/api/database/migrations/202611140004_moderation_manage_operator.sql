-- +goose Up
-- +sforum OnlineSafe
-- moderation.manage 归 operator（PRD2-12 权限归属）。
--
-- 背景：/admin/moderation/* 的 6 条路由声明 moderation.manage（审核设置、报告与决策列表、
-- 设置重置），但该键此前只授给 super_admin（202607110010），于是「审核设置与审计历史」
-- 实际上只有超管能用，运营岗被挡在外面。它是站点策略 + 审计面，归运营；
-- moderator 只做内容审核（moderation.review / moderation.view_ip），不拿设置与审计。
-- super_admin 的授予由 202607110010 拥有，本迁移不重复改。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

INSERT INTO role_permissions (role_id, permission_key)
SELECT roles.id, 'moderation.manage'
FROM roles
WHERE roles.key = 'operator'
ON CONFLICT DO NOTHING;

-- +goose Down
-- 只拆本迁移加的那一把：operator × moderation.manage。
-- 不按 permission_key 全删——super_admin 的同名授予属于 202607110010，
-- 全删会连带撤掉那条迁移的授权，也会让「seeds ⊆ DB」不变量失效。
DELETE FROM role_permissions
WHERE permission_key = 'moderation.manage'
  AND role_id IN (SELECT id FROM roles WHERE key = 'operator');
