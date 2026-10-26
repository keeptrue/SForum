package identity

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	store "github.com/keeptrue/sforum/apps/api/database/sqlc"
)

func (s *PostgresStore) ListRoles(ctx context.Context) ([]Role, error) {
	rows, err := s.queries.ListRoles(ctx)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}

	roles := make([]Role, 0, len(rows))
	for _, row := range rows {
		role := mapRole(row.ID, row.Key, row.Alias, row.Description, row.IsSystem, row.IsDefault, row.IsDeletable, row.IsEnabled)
		permissionKeys, err := s.listRolePermissionKeys(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		role.PermissionKeys = permissionKeys
		roles = append(roles, role)
	}
	return roles, nil
}

func (s *PostgresStore) CreateRole(ctx context.Context, input RoleInput) (Role, error) {
	row, err := s.queries.CreateRole(ctx, store.CreateRoleParams{
		Key:         input.Key,
		Alias:       input.Alias,
		Description: input.Description,
	})
	if err != nil {
		return Role{}, fmt.Errorf("create role: %w", err)
	}
	return mapRole(row.ID, row.Key, row.Alias, row.Description, row.IsSystem, row.IsDefault, row.IsDeletable, row.IsEnabled), nil
}

func (s *PostgresStore) UpdateRole(ctx context.Context, roleKey string, input RoleInput) (Role, error) {
	row, err := s.queries.UpdateRoleAlias(ctx, store.UpdateRoleAliasParams{
		Key:         roleKey,
		Alias:       input.Alias,
		Description: input.Description,
	})
	if err != nil {
		return Role{}, fmt.Errorf("update role: %w", err)
	}
	return mapRole(row.ID, row.Key, row.Alias, row.Description, row.IsSystem, row.IsDefault, row.IsDeletable, row.IsEnabled), nil
}

func (s *PostgresStore) DeleteRole(ctx context.Context, roleKey string) error {
	if err := s.queries.DeleteRoleByKey(ctx, roleKey); err != nil {
		return fmt.Errorf("delete role: %w", err)
	}
	return nil
}

func (s *PostgresStore) ReplaceRolePermissions(ctx context.Context, actorUserID int64, roleKey string, permissions []string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin role permissions tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	queries := s.queries.WithTx(tx)
	// 角色行是这份映射集合的唯一串行化根：IdentityRegistry 的插件权限审批在写
	// role_permissions 之前也取同一行的 FOR NO KEY UPDATE。两边都先拿锁，全量替换
	// 与增量审批才会排队，而不是让替换的 INSERT 撞上审批未提交的同主键行（23505）。
	var roleID int64
	if err := tx.QueryRow(ctx, `
		SELECT id FROM roles WHERE key = $1 FOR NO KEY UPDATE
	`, roleKey).Scan(&roleID); err != nil {
		return fmt.Errorf("get role for permissions: %w", err)
	}
	if err := queries.DeleteRolePermissions(ctx, roleID); err != nil {
		return fmt.Errorf("delete role permissions: %w", err)
	}
	for _, permission := range permissions {
		if err := queries.AddRolePermission(ctx, store.AddRolePermissionParams{
			RoleID:        roleID,
			PermissionKey: permission,
		}); err != nil {
			return fmt.Errorf("add role permission %s: %w", permission, err)
		}
	}

	metadata := auditMetadata(map[string]any{"roleKey": roleKey, "permissions": permissions})
	if err := queries.CreateAuditEvent(ctx, store.CreateAuditEventParams{
		ActorUserID:  nullableInt8(actorUserID),
		TargetUserID: pgtype.Int8{},
		Action:       "role.permissions.replace",
		Metadata:     metadata,
	}); err != nil {
		return fmt.Errorf("audit role permissions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit role permissions tx: %w", err)
	}
	return nil
}

func (s *PostgresStore) ReplaceUserRoles(ctx context.Context, actorUserID int64, targetUserID int64, roleKeys []string) (AdminUserDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("begin user roles tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	queries := s.queries.WithTx(tx)
	if _, err := tx.Exec(ctx, "DELETE FROM user_roles WHERE user_id = $1", targetUserID); err != nil {
		return AdminUserDetail{}, fmt.Errorf("delete user roles: %w", err)
	}
	for _, roleKey := range roleKeys {
		role, err := queries.GetRoleByKey(ctx, roleKey)
		if err != nil {
			return AdminUserDetail{}, fmt.Errorf("get role for user assignment: %w", err)
		}
		if err := queries.AssignRoleToUser(ctx, store.AssignRoleToUserParams{
			UserID: targetUserID,
			RoleID: role.ID,
		}); err != nil {
			return AdminUserDetail{}, fmt.Errorf("assign user role %s: %w", roleKey, err)
		}
	}

	metadata := auditMetadata(map[string]any{"roleKeys": roleKeys})
	if err := queries.CreateAuditEvent(ctx, store.CreateAuditEventParams{
		ActorUserID:  nullableInt8(actorUserID),
		TargetUserID: nullableInt8(targetUserID),
		Action:       "user.roles.replace",
		Metadata:     metadata,
	}); err != nil {
		return AdminUserDetail{}, fmt.Errorf("audit user roles: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AdminUserDetail{}, fmt.Errorf("commit user roles tx: %w", err)
	}
	return s.GetAdminUser(ctx, targetUserID)
}

func (s *PostgresStore) ReplaceUserPermissionOverrides(ctx context.Context, actorUserID int64, targetUserID int64, overrides PermissionOverrides) (AdminUserDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("begin user permission overrides tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	queries := s.queries.WithTx(tx)
	if _, err := tx.Exec(ctx, "DELETE FROM user_permission_overrides WHERE user_id = $1", targetUserID); err != nil {
		return AdminUserDetail{}, fmt.Errorf("delete user permission overrides: %w", err)
	}

	for _, permission := range overrides.Allow {
		if err := insertUserPermissionOverride(ctx, tx, actorUserID, targetUserID, permission, "allow"); err != nil {
			return AdminUserDetail{}, err
		}
	}
	for _, permission := range overrides.Deny {
		if err := insertUserPermissionOverride(ctx, tx, actorUserID, targetUserID, permission, "deny"); err != nil {
			return AdminUserDetail{}, err
		}
	}

	metadata := auditMetadata(map[string]any{"allow": overrides.Allow, "deny": overrides.Deny})
	if err := queries.CreateAuditEvent(ctx, store.CreateAuditEventParams{
		ActorUserID:  nullableInt8(actorUserID),
		TargetUserID: nullableInt8(targetUserID),
		Action:       "user.permissions.replace",
		Metadata:     metadata,
	}); err != nil {
		return AdminUserDetail{}, fmt.Errorf("audit user permissions: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AdminUserDetail{}, fmt.Errorf("commit user permission overrides tx: %w", err)
	}
	return s.GetAdminUser(ctx, targetUserID)
}

func (s *PostgresStore) RecordLoginAudit(ctx context.Context, input LoginAudit) error {
	action := input.Action
	if action == "" {
		action = AuditActionLogin
	}

	metadata := auditMetadata(map[string]any{
		"ipAddress":   input.IPAddress,
		"userAgent":   input.UserAgent,
		"sessionHash": input.SessionHash,
	})
	if err := s.queries.CreateAuditEvent(ctx, store.CreateAuditEventParams{
		ActorUserID:  nullableInt8(input.UserID),
		TargetUserID: nullableInt8(input.UserID),
		Action:       action,
		Metadata:     metadata,
	}); err != nil {
		return fmt.Errorf("audit login: %w", err)
	}
	return nil
}

func (s *PostgresStore) loadCurrentUserAccess(ctx context.Context, current *CurrentUser) error {
	return loadCurrentUserAccess(ctx, s.queries, current)
}

func (s *postgresTxStore) LoadCurrentUserAccess(ctx context.Context, current *CurrentUser) error {
	return loadCurrentUserAccess(ctx, s.queries, current)
}

func (s *PostgresStore) loadAdminUserAccess(ctx context.Context, detail *AdminUserDetail) error {
	roleKeys, err := s.listAssignedUserRoleKeys(ctx, detail.ID)
	if err != nil {
		return err
	}
	permissions, err := s.queries.ListUserPermissions(ctx, detail.ID)
	if err != nil {
		return fmt.Errorf("list admin user permissions: %w", err)
	}
	overrides, err := s.listPermissionOverrides(ctx, detail.ID)
	if err != nil {
		return err
	}
	detail.RoleKeys = roleKeys
	if permissions == nil {
		permissions = []string{}
	}
	// 展开父权限子项，保证后台菜单与 API 细粒度检查对旧角色仍可用。
	detail.Permissions = ExpandEffectivePermissions(permissions)
	detail.PermissionOverrides = overrides
	return nil
}

func loadCurrentUserAccess(ctx context.Context, queries *store.Queries, current *CurrentUser) error {
	roleKeys, err := queries.ListUserRoleKeys(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("list current user roles: %w", err)
	}
	permissions, err := queries.ListUserPermissions(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("list current user permissions: %w", err)
	}
	if roleKeys == nil {
		roleKeys = []string{}
	}
	if permissions == nil {
		permissions = []string{}
	}
	current.RoleKeys = roleKeys
	// 会话有效权限展开父→子，前端 can('settings.mail.manage') 在旧 settings.manage 下也成立。
	current.Permissions = ExpandEffectivePermissions(permissions)
	return nil
}

func (s *PostgresStore) listAssignedUserRoleKeys(ctx context.Context, userID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT roles.key
		FROM user_roles
		JOIN roles ON roles.id = user_roles.role_id
		WHERE user_roles.user_id = $1
		ORDER BY roles.key
	`, userID)
	if err != nil {
		return nil, fmt.Errorf("list assigned user roles: %w", err)
	}
	defer rows.Close()

	roleKeys := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan assigned user role: %w", err)
		}
		roleKeys = append(roleKeys, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assigned user roles: %w", err)
	}
	return roleKeys, nil
}

func (s *PostgresStore) listRolePermissionKeys(ctx context.Context, roleID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT permission_key
		FROM role_permissions
		WHERE role_id = $1
		ORDER BY permission_key
	`, roleID)
	if err != nil {
		return nil, fmt.Errorf("list role permissions: %w", err)
	}
	defer rows.Close()

	permissions := []string{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan role permission: %w", err)
		}
		permissions = append(permissions, key)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate role permissions: %w", err)
	}
	return permissions, nil
}

func (s *PostgresStore) listPermissionOverrides(ctx context.Context, userID int64) (PermissionOverrides, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT permission_key, effect
		FROM user_permission_overrides
		WHERE user_id = $1
		ORDER BY permission_key
	`, userID)
	if err != nil {
		return PermissionOverrides{}, fmt.Errorf("list user permission overrides: %w", err)
	}
	defer rows.Close()

	overrides := PermissionOverrides{Allow: []string{}, Deny: []string{}}
	for rows.Next() {
		var permission string
		var effect string
		if err := rows.Scan(&permission, &effect); err != nil {
			return PermissionOverrides{}, fmt.Errorf("scan user permission override: %w", err)
		}
		switch effect {
		case "allow":
			overrides.Allow = append(overrides.Allow, permission)
		case "deny":
			overrides.Deny = append(overrides.Deny, permission)
		}
	}
	if err := rows.Err(); err != nil {
		return PermissionOverrides{}, fmt.Errorf("iterate user permission overrides: %w", err)
	}
	return overrides, nil
}

func insertUserPermissionOverride(ctx context.Context, tx pgx.Tx, actorUserID int64, targetUserID int64, permission string, effect string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO user_permission_overrides (user_id, permission_key, effect, updated_by_user_id)
		VALUES ($1, $2, $3, $4)
	`, targetUserID, permission, effect, nullableInt8(actorUserID))
	if err != nil {
		return fmt.Errorf("insert user permission override %s:%s: %w", permission, effect, err)
	}
	return nil
}
