package systemactor

import (
	"context"
	"errors"
	"fmt"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// ErrNoUsers 表示 Host 还不能创建系统账号：库里没有任何用户。
// 自动更新通道必须先让人类完成首注册，否则系统账号会让首注册者失去
// initial super_admin 资格。
var ErrNoUsers = errors.New("extensions: system actor requires an existing user")

var errCreateConflict = errors.New("extensions: system actor create conflict")

// Spec 描述一个无密码、不可登录的 Host 系统账号。
type Spec struct {
	Username    string
	Email       string
	DisplayName string
	RoleKey     string
	// RequireExistingUser 为 true 时，只有库里已存在至少一个用户才创建。
	// 生产自动更新通道必须为 true；dev 自动机保留旧行为。
	RequireExistingUser bool
}

// Store 是系统账号所需的最小身份写入边界。
type Store interface {
	identity.ActorStore
	AnyUserExists(ctx context.Context) (bool, error)
	GetCurrentUserByEmail(ctx context.Context, email string) (identity.CurrentUser, error)
	GetCredentialByLogin(ctx context.Context, login string) (identity.CredentialUser, error)
	WithBootstrapTx(ctx context.Context, fn func(context.Context, identity.TxStore) error) error
}

// Ensure 返回（必要时创建）系统账号的 actor，并确保它持有目标角色。
// 账号不写 password credential，因此无法通过登录接口取得会话。
func Ensure(ctx context.Context, store Store, spec Spec) (identity.Actor, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return identity.Actor{}, err
	}
	if current, err := store.GetCurrentUserByEmail(ctx, spec.Email); err == nil {
		if err := ensureAdoptable(ctx, store, current, spec); err != nil {
			return identity.Actor{}, err
		}
		if err := ensureRole(ctx, store, current.ID, spec.RoleKey); err != nil {
			return identity.Actor{}, err
		}
		return store.LoadActor(ctx, current.ID)
	} else if !errors.Is(err, identity.ErrUserNotFound) {
		return identity.Actor{}, err
	}

	var userID int64
	txErr := store.WithBootstrapTx(ctx, func(ctx context.Context, tx identity.TxStore) error {
		if spec.RequireExistingUser {
			hasAnyUser, err := tx.AnyUserExists(ctx)
			if err != nil {
				return err
			}
			if !hasAnyUser {
				return ErrNoUsers
			}
		}
		current, createErr := tx.CreateUser(ctx, identity.CreateUserInput{
			Username:      spec.Username,
			Email:         spec.Email,
			DisplayName:   spec.DisplayName,
			EmailVerified: true,
		})
		if createErr != nil {
			return fmt.Errorf("%w: %v", errCreateConflict, createErr)
		}
		if err := assignRole(ctx, tx, current.ID, spec.RoleKey); err != nil {
			return err
		}
		userID = current.ID
		return nil
	})
	if errors.Is(txErr, errCreateConflict) {
		// 双实例并发创建：唯一索引冲突后按已存在处理，并补齐角色。
		// 仍然要过 ensureAdoptable：并发窗口里也可能是别人抢注了保留邮箱。
		current, getErr := store.GetCurrentUserByEmail(ctx, spec.Email)
		if getErr != nil {
			return identity.Actor{}, fmt.Errorf("resolve concurrent system actor: %w", getErr)
		}
		if err := ensureAdoptable(ctx, store, current, spec); err != nil {
			return identity.Actor{}, err
		}
		if err := ensureRole(ctx, store, current.ID, spec.RoleKey); err != nil {
			return identity.Actor{}, err
		}
		return store.LoadActor(ctx, current.ID)
	}
	if txErr != nil {
		return identity.Actor{}, txErr
	}
	return store.LoadActor(ctx, userID)
}

// ensureAdoptable 防止把系统邮箱误认成人类账号：注册账号通常带 member 角色，
// 带密码凭据的账号也不能被系统账号接管。这样即使有人在系统账号创建前抢先
// 注册了同名邮箱，也不会被授予 builtin_auto / dev-auto 角色。
func ensureAdoptable(
	ctx context.Context,
	store Store,
	current identity.CurrentUser,
	spec Spec,
) error {
	if current.Username != spec.Username {
		return fmt.Errorf("extensions: system actor email is owned by %q", current.Username)
	}
	for _, roleKey := range current.RoleKeys {
		if roleKey == identity.RoleMember {
			return fmt.Errorf("extensions: system actor email is owned by a member account")
		}
	}
	if _, err := store.GetCredentialByLogin(ctx, spec.Email); err == nil {
		return fmt.Errorf("extensions: system actor email is owned by a credential account")
	} else if !errors.Is(err, identity.ErrCredentialNotFound) {
		return err
	}
	return nil
}

func ensureRole(ctx context.Context, store Store, userID int64, roleKey string) error {
	return store.WithBootstrapTx(ctx, func(ctx context.Context, tx identity.TxStore) error {
		return assignRole(ctx, tx, userID, roleKey)
	})
}

func assignRole(ctx context.Context, tx identity.TxStore, userID int64, roleKey string) error {
	role, err := tx.GetRole(ctx, roleKey)
	if err != nil {
		return err
	}
	return tx.AssignRole(ctx, userID, role.ID)
}

func normalizeSpec(spec Spec) (Spec, error) {
	spec.Username = strings.TrimSpace(spec.Username)
	spec.Email = strings.TrimSpace(spec.Email)
	spec.DisplayName = strings.TrimSpace(spec.DisplayName)
	spec.RoleKey = strings.TrimSpace(spec.RoleKey)
	switch {
	case spec.Username == "":
		return Spec{}, errors.New("extensions: system actor username is required")
	case spec.Email == "" || !strings.Contains(spec.Email, "@"):
		return Spec{}, errors.New("extensions: system actor email is required")
	case spec.DisplayName == "":
		return Spec{}, errors.New("extensions: system actor display name is required")
	case spec.RoleKey == "":
		return Spec{}, errors.New("extensions: system actor role is required")
	}
	return spec, nil
}
