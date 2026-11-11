package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

var ErrActorInactive = errors.New("identity: actor is missing or inactive")

type UserAuthorityPairLock struct {
	ActorExists  bool
	TargetExists bool
}

// LockUserAuthorityPairTx serializes actor and target authority changes in a
// stable order. Missing rows are reported only after the caller has rechecked
// the actor, so a stale or revoked actor cannot probe target existence.
func LockUserAuthorityPairTx(
	ctx context.Context,
	tx pgx.Tx,
	actorUserID int64,
	targetUserID int64,
) (UserAuthorityPairLock, error) {
	if ctx == nil || tx == nil || actorUserID <= 0 || targetUserID <= 0 {
		return UserAuthorityPairLock{}, ErrInvalidUserUpdate
	}
	rows, err := tx.Query(ctx, `
		SELECT id
		FROM users
		WHERE id = $1 OR id = $2
		ORDER BY id
		FOR UPDATE
	`, actorUserID, targetUserID)
	if err != nil {
		return UserAuthorityPairLock{}, fmt.Errorf("lock user authority pair: %w", err)
	}
	defer rows.Close()

	result := UserAuthorityPairLock{}
	for rows.Next() {
		var userID int64
		if err := rows.Scan(&userID); err != nil {
			return UserAuthorityPairLock{}, fmt.Errorf("scan user authority pair: %w", err)
		}
		if userID == actorUserID {
			result.ActorExists = true
		}
		if userID == targetUserID {
			result.TargetExists = true
		}
	}
	if err := rows.Err(); err != nil {
		return UserAuthorityPairLock{}, fmt.Errorf("iterate user authority pair: %w", err)
	}
	return result, nil
}

func loadAuthorizedAdminActorTx(
	ctx context.Context,
	tx pgx.Tx,
	pair UserAuthorityPairLock,
	actorUserID int64,
	permission string,
	trust TrustLevelSource,
) (Actor, error) {
	if !pair.ActorExists {
		return Actor{}, ErrPermissionDenied
	}
	actor, err := LoadEffectiveActorTx(ctx, tx, actorUserID, trust)
	if errors.Is(err, ErrActorInactive) {
		return Actor{}, ErrPermissionDenied
	}
	if err != nil {
		return Actor{}, err
	}
	if !actor.Can(permission) {
		return Actor{}, ErrPermissionDenied
	}
	return actor, nil
}

func userHasAssignedRoleTx(ctx context.Context, tx pgx.Tx, userID int64, roleKey string) (bool, error) {
	rows, err := tx.Query(ctx, `
		SELECT roles.key
		FROM user_roles
		JOIN roles ON roles.id = user_roles.role_id
		WHERE user_roles.user_id = $1 AND roles.key = $2
		FOR SHARE OF user_roles, roles
	`, userID, roleKey)
	if err != nil {
		return false, fmt.Errorf("load assigned user role: %w", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, fmt.Errorf("iterate assigned user role: %w", err)
		}
		return false, nil
	}
	var found string
	if err := rows.Scan(&found); err != nil {
		return false, fmt.Errorf("scan assigned user role: %w", err)
	}
	return found == roleKey, nil
}

// LoadEffectiveActorTx resolves current Host RBAC inside the caller's
// transaction. Consumers must not authorize from a session or preflight copy
// when the protected effect is committed later in this transaction.
func LoadEffectiveActorTx(ctx context.Context, tx pgx.Tx, userID int64, trust TrustLevelSource) (Actor, error) {
	if ctx == nil || tx == nil || userID <= 0 {
		return Actor{}, ErrActorInactive
	}
	actor := Actor{ID: userID, Permissions: make(map[string]bool)}
	var kindRaw string
	if err := tx.QueryRow(ctx, `
		SELECT status, kind
		FROM users
		WHERE id = $1
		FOR SHARE
	`, userID).Scan(&actor.Status, &kindRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Actor{}, ErrActorInactive
		}
		return Actor{}, fmt.Errorf("load effective actor: %w", err)
	}
	if !actor.IsActive() {
		return Actor{}, ErrActorInactive
	}
	actor.Kind = UserKindHuman
	if ValidUserKind(UserKind(kindRaw)) {
		actor.Kind = UserKind(kindRaw)
	}

	roleRows, err := tx.Query(ctx, `
		SELECT roles.key
		FROM user_roles
		JOIN roles ON roles.id = user_roles.role_id
		WHERE user_roles.user_id = $1 AND roles.is_enabled = TRUE
		ORDER BY roles.key
		FOR SHARE OF user_roles, roles
	`, userID)
	if err != nil {
		return Actor{}, fmt.Errorf("load effective actor roles: %w", err)
	}
	for roleRows.Next() {
		var roleKey string
		if err := roleRows.Scan(&roleKey); err != nil {
			roleRows.Close()
			return Actor{}, fmt.Errorf("scan effective actor role: %w", err)
		}
		actor.RoleKeys = append(actor.RoleKeys, roleKey)
	}
	if err := roleRows.Err(); err != nil {
		roleRows.Close()
		return Actor{}, fmt.Errorf("iterate effective actor roles: %w", err)
	}
	roleRows.Close()

	permissionRows, err := tx.Query(ctx, `
		SELECT permissions.key
		FROM user_roles
		JOIN roles ON roles.id = user_roles.role_id
		JOIN role_permissions ON role_permissions.role_id = roles.id
		JOIN permissions ON permissions.key = role_permissions.permission_key
		WHERE user_roles.user_id = $1 AND roles.is_enabled = TRUE
		FOR SHARE OF user_roles, roles, role_permissions, permissions
	`, userID)
	if err != nil {
		return Actor{}, fmt.Errorf("load effective actor permissions: %w", err)
	}
	for permissionRows.Next() {
		var permission string
		if err := permissionRows.Scan(&permission); err != nil {
			permissionRows.Close()
			return Actor{}, fmt.Errorf("scan effective actor permission: %w", err)
		}
		actor.Permissions[permission] = true
	}
	if err := permissionRows.Err(); err != nil {
		permissionRows.Close()
		return Actor{}, fmt.Errorf("iterate effective actor permissions: %w", err)
	}
	permissionRows.Close()

	overrideRows, err := tx.Query(ctx, `
		SELECT permission_key, effect
		FROM user_permission_overrides
		WHERE user_id = $1
		FOR SHARE
	`, userID)
	if err != nil {
		return Actor{}, fmt.Errorf("load effective actor permission overrides: %w", err)
	}
	denied := make(map[string]bool)
	for overrideRows.Next() {
		var permission, effect string
		if err := overrideRows.Scan(&permission, &effect); err != nil {
			overrideRows.Close()
			return Actor{}, fmt.Errorf("scan effective actor permission override: %w", err)
		}
		if effect == "allow" {
			actor.Permissions[permission] = true
		} else if effect == "deny" {
			delete(actor.Permissions, permission)
			denied[permission] = true
		}
	}
	if err := overrideRows.Err(); err != nil {
		overrideRows.Close()
		return Actor{}, fmt.Errorf("iterate effective actor permission overrides: %w", err)
	}
	overrideRows.Close()

	// 信任等级已挣得来源：与 LoadActor 走同一套合并与同一套失败姿态（D22）。
	//
	// 注意这里**不做**父→子展开——那是这个 loader 的既有缺口（extension.manage
	// 持有者在插件命令路径今天就被误拒），修它会改变既有授权行为，超出本刀范围。
	// 因此跨 loader 的一致性只保证「earned 键集合相等」，不保证完整有效集相等。
	if level, granted, ok := resolveTrustGrants(ctx, userID, actor.Kind, actor.CreatedAt, trust); ok {
		actor.TrustLevel = level
		actor.TrustLevelResolved = true
		for _, key := range granted {
			if key == "" || denied[key] || actor.Permissions[key] {
				continue
			}
			actor.Permissions[key] = true
			if actor.Earned == nil {
				actor.Earned = make(map[string]bool)
			}
			actor.Earned[key] = true
		}
	}
	return actor, nil
}
