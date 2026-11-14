package trustlevels

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LedgerEntry 是变更账本 `user_trust_levels` 的一行。
//
// **它不是权威**：授权永远走热路径实时求值（D2）。账本只回答一个问题——
// 「这个用户上次完整求值算出来是几档」，好让 job 知道这次该不该发事件。
type LedgerEntry struct {
	Level      int
	TierID     string
	ComputedAt time.Time
}

// LedgerStore 是变更账本的读写面。
//
// 只有 job 路径需要它；HTTP 装载路径**不得**读账本（读了就会有人顺手拿它当等级来源，
// 于是「库里 2 档、现算 3 档」的双真相就回来了）。
type LedgerStore interface {
	// LoadLedger 返回账本行；没有行时 found=false 且 err=nil（首见用户不是错误）。
	LoadLedger(ctx context.Context, userID int64) (entry LedgerEntry, found bool, err error)
	// SaveLedger 写入本次**完整**求值的结果（upsert）。
	SaveLedger(ctx context.Context, userID int64, entry LedgerEntry) error
}

// PostgresLedgerStore 是 LedgerStore 的真库实现。
type PostgresLedgerStore struct {
	pool *pgxpool.Pool
}

func NewPostgresLedgerStore(pool *pgxpool.Pool) *PostgresLedgerStore {
	return &PostgresLedgerStore{pool: pool}
}

func (s *PostgresLedgerStore) LoadLedger(ctx context.Context, userID int64) (LedgerEntry, bool, error) {
	if s == nil || s.pool == nil {
		return LedgerEntry{}, false, errors.New("trustlevels: ledger store is not configured")
	}
	var entry LedgerEntry
	err := s.pool.QueryRow(ctx,
		`SELECT level, tier_id, computed_at FROM public.user_trust_levels WHERE user_id = $1`,
		userID).Scan(&entry.Level, &entry.TierID, &entry.ComputedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LedgerEntry{}, false, nil
	}
	if err != nil {
		return LedgerEntry{}, false, fmt.Errorf("load trust level ledger: %w", err)
	}
	return entry, true, nil
}

// SaveLedger 写入一行；computed_at 用调用方给的时刻而不是库里的 now()。
//
// 为什么：事件载荷里的 computed_at 必须是「这次求值的时刻」，否则订阅方对不上账。
func (s *PostgresLedgerStore) SaveLedger(ctx context.Context, userID int64, entry LedgerEntry) error {
	if s == nil || s.pool == nil {
		return errors.New("trustlevels: ledger store is not configured")
	}
	// 历史档位 id 可能已经从配置里消失：这里只存字符串，不做引用完整性（无可引用表）。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO public.user_trust_levels (user_id, level, tier_id, computed_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id) DO UPDATE
		SET level = EXCLUDED.level, tier_id = EXCLUDED.tier_id, computed_at = EXCLUDED.computed_at`,
		userID, entry.Level, entry.TierID, entry.ComputedAt.UTC()); err != nil {
		return fmt.Errorf("save trust level ledger: %w", err)
	}
	return nil
}

// UserTrustInput 是判定需要的 users 两列（R7：与装载路径读的是同一行）。
type UserTrustInput struct {
	Kind      string
	CreatedAt time.Time
}

// UserTrustInputReader 读一个用户的判定输入。identity 的装载路径在
// `applyTrustLevel` 里读同一行；job 路径没有 Actor，所以单独给一个窄读口。
type UserTrustInputReader interface {
	TrustInput(ctx context.Context, userID int64) (UserTrustInput, error)
}

// PostgresUserTrustInputReader 是 UserTrustInputReader 的真库实现。
type PostgresUserTrustInputReader struct {
	pool *pgxpool.Pool
}

func NewPostgresUserTrustInputReader(pool *pgxpool.Pool) *PostgresUserTrustInputReader {
	return &PostgresUserTrustInputReader{pool: pool}
}

func (r *PostgresUserTrustInputReader) TrustInput(ctx context.Context, userID int64) (UserTrustInput, error) {
	if r == nil || r.pool == nil {
		return UserTrustInput{}, errors.New("trustlevels: user trust input reader is not configured")
	}
	var input UserTrustInput
	if err := r.pool.QueryRow(ctx,
		`SELECT kind, created_at FROM users WHERE id = $1`, userID).
		Scan(&input.Kind, &input.CreatedAt); err != nil {
		return UserTrustInput{}, fmt.Errorf("load user trust input: %w", err)
	}
	return input, nil
}
