package apitokens

import (
	"errors"
	"time"
)

var (
	ErrTokenNotFound   = errors.New("apitokens: token not found")
	ErrTokenInvalid    = errors.New("apitokens: invalid token")
	ErrTokenRevoked    = errors.New("apitokens: token revoked")
	ErrTokenExpired    = errors.New("apitokens: token expired")
	ErrInvalidInput    = errors.New("apitokens: invalid input")
	ErrScopeNotAllowed = errors.New("apitokens: scope not allowed for user")
	// ErrSuperAdminTokenLocked：目标账号是 super_admin，操作者不是。
	// 与 identity.ErrSuperAdminSessionLocked 同口径（管理面保护对称）。
	ErrSuperAdminTokenLocked = errors.New("apitokens: super admin tokens cannot be revoked by non-super-admin")
)

// Token 是列表/详情用的公开视图（永不含明文）。
type Token struct {
	ID         int64      `json:"id"`
	PublicID   string     `json:"publicId"`
	Name       string     `json:"name"`
	Scopes     []string   `json:"scopes"`
	LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	ExpiresAt  *time.Time `json:"expiresAt,omitempty"`
	RevokedAt  *time.Time `json:"revokedAt,omitempty"`
	CreatedAt  time.Time  `json:"createdAt"`
	// Prefix 展示用短前缀，便于用户辨认。
	Prefix string `json:"prefix"`
}

// CreatedToken 创建/轮换时一次性返回明文。
type CreatedToken struct {
	Token
	// Plaintext 仅此次响应出现，格式 sft_<publicId>_<secret>
	Plaintext string `json:"token"`
}

type CreateInput struct {
	Name      string
	Scopes    []string
	ExpiresAt *time.Time
}

// 令牌撤销原因（写入 api_tokens.revoke_reason）。与
// user_sessions.revoke_reason 同口径，便于审计把「设备」与「令牌」放在一起读。
const (
	RevokeReasonSelf          = "revoke_self"
	RevokeReasonAdmin         = "admin_revoke"
	RevokeReasonPasswordReset = "password_reset"
)

type Record struct {
	ID         int64
	UserID     int64
	PublicID   string
	TokenHash  string
	Name       string
	Scopes     []string
	LastUsedAt *time.Time
	ExpiresAt  *time.Time
	RevokedAt  *time.Time
	CreatedAt  time.Time
	// TokenVersion 是签发时的 users.current_token_version。鉴权时与用户当前版本
	// 比对，不一致即失效：强制下线/改密都会递增版本（P2-2）。
	TokenVersion int64
	// RevokeReason 与 user_sessions.revoke_reason 同口径（admin_revoke /
	// password_reset / ...），用于审计「这条令牌为什么没了」。
	RevokeReason string
}
