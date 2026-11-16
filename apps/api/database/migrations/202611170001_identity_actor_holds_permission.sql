-- +goose Up
-- +sforum OnlineSafe
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

-- R8-BE-06 剩余一半：角色建议批准路径的授予不变量。
--
-- 为什么需要这条迁移：决定角色建议的 trigger（202607160029 建立的
-- validate_extension_permission_role_suggestion_decision）只校验「操作者持有
-- role.manage」，不校验「操作者自己已持有被建议的权限」。于是持有 role.manage 的
-- 运营者可以批准一条「给 member 加 database.manage」的建议，再以 member 身份重新
-- 登录取得该权限 —— 与已修的 BE2-01（建号铸权）同构的横向提权。
--
-- 为什么是 SQL 函数而不是 Go 判定：有效权限语义（deny 优先、override allow、
-- 生效角色）已经在 extension_identity_actor_can_manage_roles 里有一份权威实现，
-- 由 trigger 与 Go 共用。在 Go 里再写一遍必然与 SQL 漂移；这里把同一个谓词参数化，
-- 只把硬编码的 'role.manage' 换成 candidate_permission_key。
--
-- 刻意不修改已应用的 extension_identity_actor_can_manage_roles 与那个 trigger：
-- 存量库已经依赖它的形状，替换会扩大回滚面；新函数是纯增量。
--
-- 部署顺序（必须）：迁移先于二进制。新二进制在批准路径上直接调用本函数，函数不存在
-- 时是 SQL 错误 → 该端点 500；先迁后发时老二进制根本不调用它，因此天然向后兼容。
-- 反向（先发二进制后迁移）会在迁移前的窗口里让角色建议批准整体 500。
--
-- 语义（与 202607170034 的 can_manage_roles 逐字同构）：
--   super_admin 生效角色 ∥ (无 deny ∧ (override allow ∨ 生效角色授予))
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION extension_identity_actor_holds_permission(
  candidate_user_id BIGINT,
  candidate_permission_key TEXT
)
RETURNS BOOLEAN
LANGUAGE sql
STABLE
AS $$
  SELECT EXISTS (
    SELECT 1
    FROM users AS actor
    WHERE actor.id = candidate_user_id
      AND actor.status = 'active'
      AND (
        EXISTS (
          SELECT 1
          FROM user_roles
          JOIN roles ON roles.id = user_roles.role_id
          WHERE user_roles.user_id = actor.id
            AND roles.key = 'super_admin'
            AND roles.is_enabled = TRUE
        )
        OR (
          NOT EXISTS (
            SELECT 1
            FROM user_permission_overrides
            WHERE user_id = actor.id
              AND permission_key = candidate_permission_key
              AND effect = 'deny'
          )
          AND (
            EXISTS (
              SELECT 1
              FROM user_permission_overrides
              WHERE user_id = actor.id
                AND permission_key = candidate_permission_key
                AND effect = 'allow'
            )
            OR EXISTS (
              SELECT 1
              FROM user_roles
              JOIN roles ON roles.id = user_roles.role_id
              JOIN role_permissions ON role_permissions.role_id = roles.id
              WHERE user_roles.user_id = actor.id
                AND roles.is_enabled = TRUE
                AND role_permissions.permission_key = candidate_permission_key
            )
          )
        )
      )
  );
$$;
-- +goose StatementEnd

-- +goose Down
-- 纯增量函数：Down 直接移除，不留残余授权面。调用方（IdentityRegistry 的批准路径）
-- 与迁移同批发布，回滚时先回滚二进制再回滚本迁移。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '1min';

DROP FUNCTION IF EXISTS extension_identity_actor_holds_permission(BIGINT, TEXT);
