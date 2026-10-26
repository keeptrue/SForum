package identity

import (
	"context"

	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Store interface {
	ActorStore
	WithBootstrapTx(ctx context.Context, fn func(context.Context, TxStore) error) error
	AnyUserExists(ctx context.Context) (bool, error)
	FindRegistrationConflicts(ctx context.Context, username string, email string) (RegistrationConflicts, error)
	GetCurrentUser(ctx context.Context, userID int64) (CurrentUser, error)
	// GetCurrentUserByEmail 按邮箱加载用户（不要求 password credential；供 external-only 重置）。
	// 不存在时返回 ErrUserNotFound。
	GetCurrentUserByEmail(ctx context.Context, email string) (CurrentUser, error)
	GetCredentialByLogin(ctx context.Context, login string) (CredentialUser, error)
	ListPermissions(ctx context.Context) ([]Permission, error)
	ListPermissionMatrix(ctx context.Context) (PermissionMatrix, error)
	ListUsers(ctx context.Context, input UserListInput) (AdminUserList, error)
	GetAdminUser(ctx context.Context, userID int64) (AdminUserDetail, error)
	// UserKindOf 返回账号类型，供「他是不是机器人」这类服务端判断使用：
	// 登录拒绝、防循环、头像标识都问同一个问题。
	UserKindOf(ctx context.Context, userID int64) (UserKind, error)
	// UpdateAdminUser 更新用户账户字段与资料；input 中非空指针字段才会写入。
	UpdateAdminUser(ctx context.Context, actorUserID int64, targetUserID int64, input AdminUpdateUserInput) (AdminUserDetail, error)
	ListRoles(ctx context.Context) ([]Role, error)
	CreateRole(ctx context.Context, input RoleInput) (Role, error)
	UpdateRole(ctx context.Context, roleKey string, input RoleInput) (Role, error)
	DeleteRole(ctx context.Context, roleKey string) error
	ReplaceRolePermissions(ctx context.Context, actorUserID int64, roleKey string, permissions []string) error
	ReplaceUserRoles(ctx context.Context, actorUserID int64, targetUserID int64, roleKeys []string) (AdminUserDetail, error)
	ReplaceUserPermissionOverrides(ctx context.Context, actorUserID int64, targetUserID int64, overrides PermissionOverrides) (AdminUserDetail, error)
	RecordLoginAudit(ctx context.Context, input LoginAudit) error
	// 密码重置：创建令牌、消费令牌、更新密码哈希。
	CreatePasswordResetToken(ctx context.Context, input CreatePasswordResetTokenInput) (PasswordResetToken, error)
	ConsumePasswordResetToken(ctx context.Context, tokenHash string) (int64, error)
	UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error
	// ConfirmPasswordResetAtomic 在同一事务中：消费令牌、更新密码、递增 token version、撤销会话。
	ConfirmPasswordResetAtomic(ctx context.Context, tokenHash string, passwordHash string, revokeReason string) (userID int64, err error)
	// 令牌版本号：用于密码重置/封禁后使旧会话失效（M8）。
	GetUserTokenVersion(ctx context.Context, userID int64) (int64, error)
	IncrementUserTokenVersion(ctx context.Context, userID int64) error
	// 会话目录：登记/查询/下线设备（user_sessions 表）。
	// CreateSession 直接满足 authsession.SessionStore 接口（结构化匹配）。
	CreateSession(ctx context.Context, input authsession.SessionRecordInput) error
	IsSessionRevoked(ctx context.Context, userID int64, sid string) (bool, error)
	ListUserSessions(ctx context.Context, userID int64, currentSID string, includeHistory bool, page int, perPage int) (SessionListResult, error)
	RevokeSession(ctx context.Context, userID int64, sid string, reason string) error
	RevokeOtherSessions(ctx context.Context, userID int64, currentSID string, reason string) (int, error)
	// RevokeUserSessions 下线某用户的全部活跃会话（管理员强制下线），返回下线条数。
	RevokeUserSessions(ctx context.Context, userID int64, reason string) (int, error)
	EnforceMaxSessions(ctx context.Context, userID int64, currentSID string, maxDevices int) (int, error)
	TouchSessionLastSeen(ctx context.Context, userID int64, sid string) error
	// HasSessionFingerprint 判断该用户是否有活跃会话匹配给定指纹（当前按 user_agent_raw 等值匹配）。
	// 用于风险登录：未命中表示新设备。调用方应传入与 CreateSession 时一致的（已截断的）UA。
	HasSessionFingerprint(ctx context.Context, userID int64, fingerprint string) (bool, error)
	// DeleteOldRevokedSessions 清理已下线超过保留期的历史会话行（periodic job 调用）。
	DeleteOldRevokedSessions(ctx context.Context, keepDays int) (int, error)
	// ClearUserClientIPs 清空该用户相关的真实客户端 IP（隐私/删号/封禁后调用）。
	// 含：会话目录 ip_address/ip_prefix、其作者主题/评论的 ip_address 与 last_edit_ip。
	ClearUserClientIPs(ctx context.Context, userID int64) (ClearUserClientIPsResult, error)
}

type ActorStore interface {
	LoadActor(ctx context.Context, userID int64) (Actor, error)
}

// CurrentUserLocaleStore is kept separate from Store so older test seams that
// do not exercise account-language changes stay focused on their own behavior.
type CurrentUserLocaleStore interface {
	UpdateCurrentUserLocale(ctx context.Context, userID int64, locale string) (CurrentUser, error)
}

type TxStore interface {
	AnyUserExists(ctx context.Context) (bool, error)
	FindRegistrationConflicts(ctx context.Context, username string, email string) (RegistrationConflicts, error)
	CreateUser(ctx context.Context, input CreateUserInput) (CurrentUser, error)
	CreateCredential(ctx context.Context, userID int64, passwordHash string) error
	GetDefaultRole(ctx context.Context) (Role, error)
	GetRole(ctx context.Context, roleKey string) (Role, error)
	AssignRole(ctx context.Context, userID int64, roleID int64) error
	LoadCurrentUserAccess(ctx context.Context, current *CurrentUser) error
}

type CreateUserInput struct {
	Username            string
	Email               string
	DisplayName         string
	Locale              string
	IsInitialSuperAdmin bool
	EmailVerified       bool
}

type RegistrationConflicts struct {
	UsernameTaken bool
	EmailTaken    bool
}

type CredentialUser struct {
	CurrentUser
	PasswordHash string
}

type Role struct {
	ID             int64    `json:"id"`
	Key            string   `json:"key"`
	Alias          string   `json:"alias"`
	Description    string   `json:"description"`
	IsSystem       bool     `json:"isSystem"`
	IsDefault      bool     `json:"isDefault"`
	IsDeletable    bool     `json:"isDeletable"`
	IsEnabled      bool     `json:"isEnabled"`
	PermissionKeys []string `json:"permissionKeys"`
}

type RoleInput struct {
	Key         string
	Alias       string
	Description string
}
