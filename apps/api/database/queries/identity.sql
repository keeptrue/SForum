-- name: AnyUserExists :one
SELECT EXISTS (SELECT 1 FROM users LIMIT 1)::boolean;

-- name: FindRegistrationConflicts :one
SELECT
  EXISTS (SELECT 1 FROM users WHERE username_lower = lower($1))::boolean AS username_taken,
  EXISTS (SELECT 1 FROM users WHERE email_lower = lower($2))::boolean AS email_taken;

-- name: CreateUser :one
INSERT INTO users (username, username_lower, email, email_lower, display_name, locale, is_initial_super_admin, email_verified_at)
VALUES ($1, lower($1), $2, lower($2), $3, $4, $5, CASE WHEN $6::boolean THEN now() ELSE NULL END)
RETURNING id, username, display_name, locale, status, is_initial_super_admin, email_verified_at IS NOT NULL AS email_verified;

-- name: CreateUserCredential :exec
INSERT INTO user_credentials (user_id, password_hash)
VALUES ($1, $2);

-- name: GetUserCredentialByLogin :one
SELECT users.id, users.username, users.display_name, users.locale, users.status, users.is_initial_super_admin, user_credentials.password_hash
FROM users
JOIN user_credentials ON user_credentials.user_id = users.id
WHERE users.username_lower = lower($1) OR users.email_lower = lower($1);

-- name: GetDefaultRole :one
SELECT id, key, alias, description, is_system, is_default, is_deletable, is_enabled
FROM roles
WHERE is_default = TRUE AND is_enabled = TRUE;

-- name: GetRoleByKey :one
SELECT id, key, alias, description, is_system, is_default, is_deletable, is_enabled
FROM roles
WHERE key = $1;

-- name: AssignRoleToUser :exec
INSERT INTO user_roles (user_id, role_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: ListUserRoleKeys :many
SELECT roles.key
FROM user_roles
JOIN roles ON roles.id = user_roles.role_id
WHERE user_roles.user_id = $1 AND roles.is_enabled = TRUE
ORDER BY roles.key;

-- name: ListUserPermissions :many
WITH role_permission_keys AS (
  SELECT DISTINCT permissions.key
  FROM user_roles
  JOIN roles ON roles.id = user_roles.role_id
  JOIN role_permissions ON role_permissions.role_id = roles.id
  JOIN permissions ON permissions.key = role_permissions.permission_key
  WHERE user_roles.user_id = $1 AND roles.is_enabled = TRUE
),
allowed_permission_keys AS (
  SELECT permission_key AS key
  FROM user_permission_overrides
  WHERE user_id = $1 AND effect = 'allow'
),
denied_permission_keys AS (
  SELECT permission_key AS key
  FROM user_permission_overrides
  WHERE user_id = $1 AND effect = 'deny'
),
candidate_permission_keys AS (
  SELECT key FROM role_permission_keys
  UNION
  SELECT key FROM allowed_permission_keys
)
-- 候选集 + 是否生效一起返回：deny 的判定仍然只在这里做一次，装载期据此
-- 实现「effective = Expand(static) ∪ (earned ∖ denied)」（计划 D14）。
-- 候选集 + 是否生效，**外加**被 deny 但没有任何静态授予的键（D28）。
-- 少了第二段，deny 一个「谁都没授予过的键」就看不见，信任等级挣得它时 deny 被绕过。
SELECT candidate_permission_keys.key,
       NOT EXISTS (
         SELECT 1
         FROM denied_permission_keys
         WHERE denied_permission_keys.key = candidate_permission_keys.key
       ) AS effective
FROM candidate_permission_keys
UNION
SELECT denied_permission_keys.key, FALSE AS effective
FROM denied_permission_keys
ORDER BY 1;

-- name: ListPermissions :many
SELECT key, module, description
FROM permissions
ORDER BY module ASC, key ASC;

-- name: GetCurrentUser :one
SELECT id, username, display_name, locale, status, is_initial_super_admin
FROM users
WHERE id = $1;

-- name: ListRoles :many
SELECT id, key, alias, description, is_system, is_default, is_deletable, is_enabled
FROM roles
ORDER BY is_system DESC, key ASC;

-- name: CreateRole :one
INSERT INTO roles (key, alias, description, is_system, is_default, is_deletable, is_enabled)
VALUES ($1, $2, $3, FALSE, FALSE, TRUE, TRUE)
RETURNING id, key, alias, description, is_system, is_default, is_deletable, is_enabled;

-- name: UpdateRoleAlias :one
UPDATE roles
SET alias = $2, description = $3, updated_at = now()
WHERE key = $1
RETURNING id, key, alias, description, is_system, is_default, is_deletable, is_enabled;

-- name: DeleteRoleByKey :exec
DELETE FROM roles
WHERE key = $1 AND is_deletable = TRUE AND is_system = FALSE;

-- name: DeleteRolePermissions :exec
DELETE FROM role_permissions
WHERE role_id = $1;

-- name: AddRolePermission :exec
INSERT INTO role_permissions (role_id, permission_key)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: CreateAuditEvent :exec
INSERT INTO audit_events (actor_user_id, target_user_id, action, metadata)
VALUES ($1, $2, $3, $4);
