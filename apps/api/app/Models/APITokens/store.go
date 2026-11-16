package apitokens

import (
	"context"
	"time"
)

type Store interface {
	Create(ctx context.Context, userID int64, publicID, tokenHash, name string, scopes []string, expiresAt *time.Time) (Record, error)
	ListByUser(ctx context.Context, userID int64, includeRevoked bool) ([]Record, error)
	GetByPublicID(ctx context.Context, publicID string) (Record, error)
	GetByIDForUser(ctx context.Context, userID, id int64) (Record, error)
	Revoke(ctx context.Context, userID, id int64) error
	// Rotate：撤销旧 token 并创建新 token（同名/scopes）。
	TouchLastUsed(ctx context.Context, id int64) error
	// TokenVersion 读该用户当前的 users.current_token_version。
	// 鉴权用它比对记录里的签发版本（P2-2）。
	TokenVersion(ctx context.Context, userID int64) (int64, error)
	// RevokeAllForUser 撤销该用户全部未撤销的令牌，返回撤销条数。
	RevokeAllForUser(ctx context.Context, userID int64, reason string) (int, error)
}
