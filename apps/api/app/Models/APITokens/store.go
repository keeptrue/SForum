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
	// TouchLastUsed 刷新令牌的最后使用时间。
	//
	// 实现必须自带写频率节流（PostgresStore 用 1 分钟时间条件，见
	// lastUsedThrottle）：鉴权路径每个请求都会调用它，无条件写入会造成写放大
	// 与热门令牌的行锁竞争（P3-1）。调用方不关心本次是否真的写了。
	TouchLastUsed(ctx context.Context, id int64) error
	// TokenVersion 读该用户当前的 users.current_token_version。
	// 鉴权用它比对记录里的签发版本（P2-2）。
	TokenVersion(ctx context.Context, userID int64) (int64, error)
	// RevokeAllForUser 撤销该用户全部未撤销的令牌，返回撤销条数。
	RevokeAllForUser(ctx context.Context, userID int64, reason string) (int, error)
}
