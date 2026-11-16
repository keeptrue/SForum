package apitokens

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) Create(ctx context.Context, userID int64, publicID, tokenHash, name string, scopes []string, expiresAt *time.Time) (Record, error) {
	scopesJSON, err := json.Marshal(scopes)
	if err != nil {
		return Record{}, err
	}
	var rec Record
	var scopesRaw []byte
	// token_version 从 users 行里取，而不是调用方传进来：读写在同一句里完成，
	// 不会出现「读到旧版本、写入时版本已被强制下线推进」的竞态（P2-2）。
	err = s.pool.QueryRow(ctx, `
INSERT INTO api_tokens (user_id, public_id, token_hash, name, scopes, expires_at, token_version)
SELECT $1, $2, $3, $4, $5, $6, users.current_token_version
-- FOR SHARE：READ COMMITTED 下普通 SELECT 读的是语句快照、不等行锁，并发
-- 「强制下线」正在递增 current_token_version 时，这里会读到旧版本并写进新 PAT，
-- 提交后该 PAT 立刻失效。加共享锁让它等对方提交，再读新值（P2-2 补刀）。
FROM users WHERE users.id = $1 FOR SHARE
RETURNING id, user_id, public_id, token_hash, name, scopes, last_used_at, expires_at, revoked_at, created_at, token_version, revoke_reason`,
		userID, publicID, tokenHash, name, scopesJSON, expiresAt,
	).Scan(&rec.ID, &rec.UserID, &rec.PublicID, &rec.TokenHash, &rec.Name, &scopesRaw,
		&rec.LastUsedAt, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt, &rec.TokenVersion, &rec.RevokeReason)
	if err != nil {
		return Record{}, err
	}
	_ = json.Unmarshal(scopesRaw, &rec.Scopes)
	if rec.Scopes == nil {
		rec.Scopes = []string{}
	}
	return rec, nil
}

func (s *PostgresStore) ListByUser(ctx context.Context, userID int64, includeRevoked bool) ([]Record, error) {
	query := `
SELECT id, user_id, public_id, token_hash, name, scopes, last_used_at, expires_at, revoked_at, created_at, token_version, revoke_reason
FROM api_tokens WHERE user_id=$1`
	if !includeRevoked {
		query += ` AND revoked_at IS NULL`
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.pool.Query(ctx, query, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRecords(rows)
}

func (s *PostgresStore) GetByPublicID(ctx context.Context, publicID string) (Record, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, user_id, public_id, token_hash, name, scopes, last_used_at, expires_at, revoked_at, created_at, token_version, revoke_reason
FROM api_tokens WHERE public_id=$1`, publicID)
	return scanRecord(row)
}

func (s *PostgresStore) GetByIDForUser(ctx context.Context, userID, id int64) (Record, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, user_id, public_id, token_hash, name, scopes, last_used_at, expires_at, revoked_at, created_at, token_version, revoke_reason
FROM api_tokens WHERE id=$1 AND user_id=$2`, id, userID)
	return scanRecord(row)
}

func (s *PostgresStore) Revoke(ctx context.Context, userID, id int64) error {
	tag, err := s.pool.Exec(ctx, `
UPDATE api_tokens SET revoked_at=NOW(), revoke_reason=$3
WHERE id=$1 AND user_id=$2 AND revoked_at IS NULL`, id, userID, RevokeReasonSelf)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}

func (s *PostgresStore) TouchLastUsed(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE api_tokens SET last_used_at=NOW() WHERE id=$1`, id)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row rowScanner) (Record, error) {
	var rec Record
	var scopesRaw []byte
	err := row.Scan(&rec.ID, &rec.UserID, &rec.PublicID, &rec.TokenHash, &rec.Name, &scopesRaw,
		&rec.LastUsedAt, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt, &rec.TokenVersion, &rec.RevokeReason)
	if err == pgx.ErrNoRows {
		return Record{}, ErrTokenNotFound
	}
	if err != nil {
		return Record{}, err
	}
	_ = json.Unmarshal(scopesRaw, &rec.Scopes)
	if rec.Scopes == nil {
		rec.Scopes = []string{}
	}
	return rec, nil
}

func scanRecords(rows pgx.Rows) ([]Record, error) {
	items := []Record{}
	for rows.Next() {
		var rec Record
		var scopesRaw []byte
		if err := rows.Scan(&rec.ID, &rec.UserID, &rec.PublicID, &rec.TokenHash, &rec.Name, &scopesRaw,
			&rec.LastUsedAt, &rec.ExpiresAt, &rec.RevokedAt, &rec.CreatedAt, &rec.TokenVersion, &rec.RevokeReason); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(scopesRaw, &rec.Scopes)
		if rec.Scopes == nil {
			rec.Scopes = []string{}
		}
		items = append(items, rec)
	}
	return items, rows.Err()
}

// TokenVersion 读该用户当前的令牌版本。鉴权路径用它比对记录里的签发版本：
// 强制下线/改密递增 users.current_token_version 之后，旧 PAT 立刻失效（P2-2）。
func (s *PostgresStore) TokenVersion(ctx context.Context, userID int64) (int64, error) {
	var version int64
	if err := s.pool.QueryRow(ctx, `
SELECT current_token_version FROM users WHERE id=$1`, userID).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			return 0, ErrTokenInvalid
		}
		return 0, err
	}
	return version, nil
}

// RevokeAllForUser 撤销该用户全部未撤销令牌（管理端「撤销全部 PAT」与强制下线共用
// 同一口径），返回撤销条数。reason 与 user_sessions.revoke_reason 同口径。
func (s *PostgresStore) RevokeAllForUser(ctx context.Context, userID int64, reason string) (int, error) {
	tag, err := s.pool.Exec(ctx, `
UPDATE api_tokens SET revoked_at=NOW(), revoke_reason=$2
WHERE user_id=$1 AND revoked_at IS NULL`, userID, reason)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}
