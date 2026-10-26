package identity

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	store "github.com/keeptrue/sforum/apps/api/database/sqlc"
)

// CreateAdminUser 在单个事务内创建用户、密码凭据、角色与审计事件。
// 任何一步失败都会整体回滚，不会留下没有凭据或没有角色的半成品账号。
func (s *PostgresStore) CreateAdminUser(ctx context.Context, actorUserID int64, input AdminCreateUserStoreInput) (AdminUserDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AdminUserDetail{}, fmt.Errorf("begin admin create user tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	queries := s.queries.WithTx(tx)
	txStore := &postgresTxStore{queries: queries, avatarBuilder: s.avatarBuilder}

	current, err := txStore.CreateUser(ctx, CreateUserInput{
		Username:      input.Username,
		Email:         input.Email,
		DisplayName:   input.DisplayName,
		Locale:        input.Locale,
		EmailVerified: input.EmailVerified,
	})
	if err != nil {
		return AdminUserDetail{}, err
	}
	if err := txStore.CreateCredential(ctx, current.ID, input.PasswordHash); err != nil {
		return AdminUserDetail{}, err
	}

	roleKeys := input.RoleKeys
	if len(roleKeys) == 0 {
		roleKeys = []string{RoleMember}
	}
	for _, roleKey := range roleKeys {
		role, err := txStore.GetRole(ctx, roleKey)
		if err != nil {
			// 预检与写入之间角色被删除：回滚并返回可映射的 422，而不是 500。
			if errors.Is(err, pgx.ErrNoRows) {
				return AdminUserDetail{}, ErrInvalidRole
			}
			return AdminUserDetail{}, err
		}
		if err := txStore.AssignRole(ctx, current.ID, role.ID); err != nil {
			return AdminUserDetail{}, err
		}
	}

	metadata := auditMetadata(map[string]any{
		"username":      input.Username,
		"email":         input.Email,
		"displayName":   input.DisplayName,
		"roleKeys":      roleKeys,
		"emailVerified": input.EmailVerified,
		"passwordMode":  input.PasswordMode,
	})
	if err := queries.CreateAuditEvent(ctx, store.CreateAuditEventParams{
		ActorUserID:  nullableInt8(actorUserID),
		TargetUserID: nullableInt8(current.ID),
		Action:       "user.admin_create",
		Metadata:     metadata,
	}); err != nil {
		return AdminUserDetail{}, fmt.Errorf("audit admin create user: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return AdminUserDetail{}, fmt.Errorf("commit admin create user tx: %w", err)
	}
	return s.GetAdminUser(ctx, current.ID)
}
