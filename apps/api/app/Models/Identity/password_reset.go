package identity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PasswordResetToken 是 password_reset_tokens 行的领域结构。
type PasswordResetToken struct {
	ID            int64
	UserID        int64
	TokenHash     string
	ExpiresAt     time.Time
	ConsumedAt    *time.Time
	CreatedAt     time.Time
	RequestIPHash string
}

// CreatePasswordResetToken 插入一条密码重置令牌。
func (s *PostgresStore) CreatePasswordResetToken(ctx context.Context, input CreatePasswordResetTokenInput) (PasswordResetToken, error) {
	row := s.pool.QueryRow(ctx, `
		INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, request_ip_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, token_hash, expires_at, consumed_at, created_at, request_ip_hash
	`, input.UserID, input.TokenHash, input.ExpiresAt, input.RequestIPHash)
	token, err := scanPasswordResetToken(row)
	if err != nil {
		return PasswordResetToken{}, fmt.Errorf("create password reset token: %w", err)
	}
	return token, nil
}

// FindValidPasswordResetToken 按令牌哈希查找未消费且未过期的令牌。
func (s *PostgresStore) FindValidPasswordResetToken(ctx context.Context, tokenHash string) (PasswordResetToken, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, user_id, token_hash, expires_at, consumed_at, created_at, request_ip_hash
		FROM password_reset_tokens
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND expires_at > now()
	`, tokenHash)
	token, err := scanPasswordResetToken(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return PasswordResetToken{}, ErrPasswordResetTokenNotFound
	}
	if err != nil {
		return PasswordResetToken{}, fmt.Errorf("find password reset token: %w", err)
	}
	return token, nil
}

// ConsumePasswordResetToken 原子性地消费令牌并返回 user_id；已消费或不存在返回错误。
func (s *PostgresStore) ConsumePasswordResetToken(ctx context.Context, tokenHash string) (int64, error) {
	var userID int64
	err := s.pool.QueryRow(ctx, `
		UPDATE password_reset_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id
	`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPasswordResetTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("consume password reset token: %w", err)
	}
	return userID, nil
}

// ConfirmPasswordResetAtomic 在同一事务内完成令牌消费、密码更新、token version 递增与会话撤销，
// 避免「令牌已消费但密码未更新」或「密码已改但旧会话仍有效」的中间态。
func (s *PostgresStore) ConfirmPasswordResetAtomic(ctx context.Context, tokenHash string, passwordHash string, revokeReason string) (int64, error) {
	var userID int64
	err := s.runIdentityAuthorityMutation(ctx, func() error {
		var resetErr error
		userID, resetErr = s.confirmPasswordResetAtomic(ctx, tokenHash, passwordHash, revokeReason)
		return resetErr
	})
	return userID, err
}

func (s *PostgresStore) confirmPasswordResetAtomic(ctx context.Context, tokenHash string, passwordHash string, revokeReason string) (int64, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, fmt.Errorf("begin password reset confirm: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var userID int64
	err = tx.QueryRow(ctx, `
		UPDATE password_reset_tokens
		SET consumed_at = now()
		WHERE token_hash = $1
		  AND consumed_at IS NULL
		  AND expires_at > now()
		RETURNING user_id
	`, tokenHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrPasswordResetTokenNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("consume password reset token: %w", err)
	}

	// external-only 用户无 credential 行：创建；已有行则更新。password_hash 始终非 null。
	if err := upsertUserPasswordTx(ctx, tx, userID, passwordHash); err != nil {
		return 0, err
	}

	if _, err := tx.Exec(ctx, `
		UPDATE users SET current_token_version = current_token_version + 1, updated_at = now()
		WHERE id = $1
	`, userID); err != nil {
		return 0, fmt.Errorf("increment user token version: %w", err)
	}

	rows, err := tx.Query(ctx, `
		UPDATE user_sessions
		SET revoked_at = now(), revoke_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL
		RETURNING sid
	`, userID, revokeReason)
	if err != nil {
		return 0, fmt.Errorf("revoke user sessions: %w", err)
	}
	revokedSIDs, err := collectRevokedSIDs(rows)
	if err != nil {
		return 0, fmt.Errorf("collect revoked sessions: %w", err)
	}

	// 改密后旧 PAT 必须一起失效，且在同一事务里：否则「改密 → 强制下线」的语义
	// 对机器客户端是空的（P2-2）。版本比对是第二道闸。
	if _, err := tx.Exec(ctx, `
		UPDATE api_tokens
		SET revoked_at = transaction_timestamp(), revoke_reason = $2
		WHERE user_id = $1 AND revoked_at IS NULL
	`, userID, revokeReason); err != nil {
		return 0, fmt.Errorf("revoke user api tokens: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit password reset confirm: %w", err)
	}
	// 改密即等于「所有旧会话结束」：插件状态同样不得残留（提交后才擦除）。
	s.dropPluginSessionState(ctx, revokedSIDs)
	return userID, nil
}

// UpdateUserPassword 更新或创建用户密码凭据（external-only 首次设置时 INSERT）。
// password_hash 对存在行始终非 null；无行表示 external-only。
func (s *PostgresStore) UpdateUserPassword(ctx context.Context, userID int64, passwordHash string) error {
	return upsertUserPassword(ctx, s.pool, userID, passwordHash)
}

func upsertUserPassword(ctx context.Context, pool *pgxpool.Pool, userID int64, passwordHash string) error {
	if userID <= 0 || strings.TrimSpace(passwordHash) == "" {
		return ErrCredentialNotFound
	}
	// INSERT … ON CONFLICT 保证 external-only 可首次创建，已有行则更新哈希。
	_, err := pool.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash, method, password_changed_at, created_at, updated_at)
		VALUES ($1, $2, 'password', now(), now(), now())
		ON CONFLICT (user_id) DO UPDATE SET
		  password_hash = EXCLUDED.password_hash,
		  method = 'password',
		  password_changed_at = now(),
		  updated_at = now()
	`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("upsert user password: %w", err)
	}
	return nil
}

func upsertUserPasswordTx(ctx context.Context, tx pgx.Tx, userID int64, passwordHash string) error {
	if userID <= 0 || strings.TrimSpace(passwordHash) == "" {
		return ErrCredentialNotFound
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO user_credentials (user_id, password_hash, method, password_changed_at, created_at, updated_at)
		VALUES ($1, $2, 'password', now(), now(), now())
		ON CONFLICT (user_id) DO UPDATE SET
		  password_hash = EXCLUDED.password_hash,
		  method = 'password',
		  password_changed_at = now(),
		  updated_at = now()
	`, userID, passwordHash)
	if err != nil {
		return fmt.Errorf("upsert user password: %w", err)
	}
	return nil
}

// GetUserTokenVersion 返回用户当前的令牌版本号，用于会话失效校验（M8）。
func (s *PostgresStore) GetUserTokenVersion(ctx context.Context, userID int64) (int64, error) {
	var version int64
	err := s.pool.QueryRow(ctx, `
		SELECT current_token_version FROM users WHERE id = $1
	`, userID).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrCredentialNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get user token version: %w", err)
	}
	return version, nil
}

// IncrementUserTokenVersion 递增用户令牌版本号，使该用户所有旧会话失效（M8）。
// 用于密码重置成功后，让攻击者已持有的旧会话不再有效。
func (s *PostgresStore) IncrementUserTokenVersion(ctx context.Context, userID int64) error {
	return s.runIdentityAuthorityMutation(ctx, func() error {
		return s.incrementUserTokenVersion(ctx, userID)
	})
}

func (s *PostgresStore) incrementUserTokenVersion(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET current_token_version = current_token_version + 1, updated_at = now()
		WHERE id = $1
	`, userID)
	if err != nil {
		return fmt.Errorf("increment user token version: %w", err)
	}
	return nil
}

// CreatePasswordResetTokenInput 是创建重置令牌的入参。
type CreatePasswordResetTokenInput struct {
	UserID        int64
	TokenHash     string
	ExpiresAt     time.Time
	RequestIPHash string
}

type passwordResetScanner interface {
	Scan(dest ...any) error
}

func scanPasswordResetToken(row passwordResetScanner) (PasswordResetToken, error) {
	var token PasswordResetToken
	var consumedAt *time.Time
	if err := row.Scan(
		&token.ID,
		&token.UserID,
		&token.TokenHash,
		&token.ExpiresAt,
		&consumedAt,
		&token.CreatedAt,
		&token.RequestIPHash,
	); err != nil {
		return PasswordResetToken{}, err
	}
	token.ConsumedAt = consumedAt
	return token, nil
}
