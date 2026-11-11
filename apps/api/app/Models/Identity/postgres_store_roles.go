package identity

import (
	"context"
	"fmt"
	"log/slog"
	"time"

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
	if err := loadCurrentUserAccess(ctx, s.queries, current); err != nil {
		return err
	}
	return s.applyTrustLevel(ctx, current)
}

// applyTrustLevel 在装载期把信任等级并进有效权限集（D13/D23/D29）。
//
// created_at 与 kind 一次读出，正好也是新人阶梯与 bot 判据需要的两列：全请求只读
// 一次（R7），不让 LoadActor 再发一条重复查询。
func (s *PostgresStore) applyTrustLevel(ctx context.Context, current *CurrentUser) error {
	if s == nil || current == nil {
		return nil
	}
	// 这一行是全请求**唯一**的 users 行读取（R7）：created_at 供新人阶梯与账龄闸，
	// kind 供 bot 判据与等级判定。LoadActor 不再重复查。
	var createdAt time.Time
	var kindRaw string
	if err := s.pool.QueryRow(ctx, `SELECT created_at, kind FROM users WHERE id = $1`, current.ID).
		Scan(&createdAt, &kindRaw); err != nil {
		// 读失败：保持未解析（D7），不阻断请求。
		return nil
	}
	current.CreatedAt = createdAt
	if ValidUserKind(UserKind(kindRaw)) {
		current.Kind = UserKind(kindRaw)
	} else {
		current.Kind = UserKindHuman
	}
	if s.trustLevels == nil {
		return nil
	}
	level, earned, ok := resolveTrustGrants(ctx, current.ID, current.Kind, current.CreatedAt, s.trustLevels)
	if !ok {
		return nil
	}
	current.TrustLevel = level
	current.trustLevelResolved = true
	// Z3-b″ 减法门：扣减与已挣得合并都在 applyCurrentUserTrustGrants 里，
	// 与事务路径共用同一份实现。allow 覆盖只在门真的会生效时才多读一次。
	_, current.earned = applyCurrentUserTrustGrants(s.trustLevels, current, level, earned,
		s.baselineAllowOverrides(ctx, current.ID, level, current.Kind))
	return nil
}

// baselineAllowOverrides 返回该用户的显式 allow 覆盖键，供减法门豁免（§4-bis 第 4 条）。
//
// 为什么必须单独读一次：`ListUserPermissions` 的行只有 {key, effective}，拿不到
// 「这个键生效是因为角色授予还是 allow 覆盖」，而给它加列要跑 `sqlc generate`
// （本仓不可安全运行，residual 4）。
//
// 为什么不是每条请求都读：门只在「已解析 + 未到 TL1 + 非 bot」时生效，其余路径
// 直接返回 nil，查询次数与本刀之前逐字相同（G 组预算不变）。
//
// 失败姿态 fail-open：读不到就当作「没有 allow 覆盖」，即**不扣**。宁可 TL0 多留
// 一个键，不可因为一次查询失败把整批新用户的发言权限挡掉（§4-bis 的失败方向）。
func (s *PostgresStore) baselineAllowOverrides(ctx context.Context, userID int64, level int, kind UserKind) map[string]bool {
	if !needsBaselineDeduction(s.trustLevels, level, kind) {
		return nil
	}
	overrides, err := s.listPermissionOverrides(ctx, userID)
	if err != nil {
		slog.Warn("identity: load allow overrides for baseline deduction failed", "userId", userID, "error", err)
		return nil
	}
	allow := make(map[string]bool, len(overrides.Allow))
	for _, key := range overrides.Allow {
		allow[key] = true
	}
	return allow
}

// postgresTxStore 的路径不合并已挣得来源：事务内的装载用于注册/改密等身份写流程，
// 那些流程里的账号要么刚创建（TL0，无已挣得），要么与等级无关。保持 nil 让这条
// 路径的行为与本刀之前逐字节一致。
func (s *postgresTxStore) LoadCurrentUserAccess(ctx context.Context, current *CurrentUser) error {
	return loadCurrentUserAccess(ctx, s.queries, current)
}

func (s *PostgresStore) loadAdminUserAccess(ctx context.Context, detail *AdminUserDetail) error {
	roleKeys, err := s.listAssignedUserRoleKeys(ctx, detail.ID)
	if err != nil {
		return err
	}
	decisions, err := s.queries.ListUserPermissions(ctx, detail.ID)
	if err != nil {
		return fmt.Errorf("list admin user permissions: %w", err)
	}
	// 后台详情展示的是「生效中的权限」：候选集里被 deny 的行不进去。
	permissions := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Effective {
			permissions = append(permissions, decision.Key)
		}
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

// loadCurrentUserAccess 是 Actor.Permissions 与 CurrentUser.Permissions 的**唯一**
// 物化点（D13）。信任等级的已挣得授予在这里合并，所以 Can() 一个字节都不用改，
// 后端裁决与下发前端的 can() 自动一致。
func loadCurrentUserAccess(ctx context.Context, queries *store.Queries, current *CurrentUser) error {
	roleKeys, err := queries.ListUserRoleKeys(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("list current user roles: %w", err)
	}
	decisions, err := queries.ListUserPermissions(ctx, current.ID)
	if err != nil {
		return fmt.Errorf("list current user permissions: %w", err)
	}
	if roleKeys == nil {
		roleKeys = []string{}
	}
	current.RoleKeys = roleKeys
	// 静态路径保持本刀之前的语义：父→子展开，deny 已在 SQL 里排除。
	current.Permissions = ExpandEffectivePermissions(effectivePermissionKeys(decisions))
	current.denied = deniedPermissionKeys(decisions)
	return nil
}

// effectivePermissionKeys 返回候选集里生效的键（deny 的键不在其中）。
func effectivePermissionKeys(decisions []store.ListUserPermissionsRow) []string {
	keys := make([]string, 0, len(decisions))
	for _, decision := range decisions {
		if decision.Effective {
			keys = append(keys, decision.Key)
		}
	}
	return keys
}

// deniedPermissionKeys 返回被显式 deny 的键——**包括没有任何静态授予的键**（D28）。
func deniedPermissionKeys(decisions []store.ListUserPermissionsRow) map[string]bool {
	denied := make(map[string]bool, len(decisions))
	for _, decision := range decisions {
		if !decision.Effective {
			denied[decision.Key] = true
		}
	}
	return denied
}

// 合并实现已收进 trust_level_grants.go 的 mergeEarnedKeys（D23）：这里原本有一份
// 独立的合并循环，减法门（Z3-b″）要在同一个地方扣键，两处并存就是本仓反复出现的
// 「两份」。CurrentUser 形态的入口现在是 applyCurrentUserTrustGrants。



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
