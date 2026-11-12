package badges

import (
	"context"
	"fmt"
	"time"
)

// FactInput 是一条插件事实（「发生了什么」）。
//
// DedupeKey 为空表示**不去重**：两次调用就是两行事实。这是刻意的——部分唯一索引
// 的条件是 `WHERE dedupe_key <> ”`，空串不参与唯一性。别为了「幂等」把空串也去重掉：
// 那样「同一秒内发生两次」的合法累加会被吞掉一次。
type FactInput struct {
	UserID            int64
	FactKey           string
	Variant           string
	Delta             int64
	SourceExtensionID string
	DedupeKey         string
	OccurredAt        time.Time
}

// FactResult 是写入结果。`Inserted=false` 表示 dedupe 命中（幂等返回，不是错误）。
type FactResult struct {
	Inserted bool  `json:"inserted"`
	Total    int64 `json:"total"`
}

// RecordFact 在同一事务里写事实行与累计计数。
//
// **M1 没有调用方**：它是 M3 的 Host 命令 `sforum.badges.fact.record` 的落地实现。
// 不要当作死代码删除——M3 接入时底层必须已经在这里。`ErrBadgeFactInvalid` 在 M1
// 也不需要错误码文案（没有 HTTP 面），M3 接入时再补码与文案。
//
// 两件事必须同事务：只写事实不更计数，窗口条件与累计条件会立刻不一致；只更计数
// 不写事实，窗口条件永远看不到这次事件。中途失败必须一起回滚。
func (s *PostgresStore) RecordFact(ctx context.Context, input FactInput) (FactResult, error) {
	if input.UserID <= 0 || input.FactKey == "" || input.Delta == 0 {
		return FactResult{}, ErrBadgeFactInvalid
	}
	occurredAt := input.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return FactResult{}, fmt.Errorf("record fact: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	inserted := true
	if input.DedupeKey != "" {
		// 部分唯一索引的冲突目标必须带上同样的谓词，否则 Postgres 推断不到那个索引。
		tag, err := tx.Exec(ctx, `
			INSERT INTO badge_facts (user_id, fact_key, variant, delta, occurred_at, source_extension_id, dedupe_key)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (user_id, fact_key, variant, dedupe_key) WHERE dedupe_key <> '' DO NOTHING`,
			input.UserID, input.FactKey, input.Variant, input.Delta, occurredAt, input.SourceExtensionID, input.DedupeKey)
		if err != nil {
			return FactResult{}, fmt.Errorf("insert fact: %w", err)
		}
		inserted = tag.RowsAffected() > 0
	} else {
		if _, err := tx.Exec(ctx, `
			INSERT INTO badge_facts (user_id, fact_key, variant, delta, occurred_at, source_extension_id, dedupe_key)
			VALUES ($1,$2,$3,$4,$5,$6,'')`,
			input.UserID, input.FactKey, input.Variant, input.Delta, occurredAt, input.SourceExtensionID); err != nil {
			return FactResult{}, fmt.Errorf("insert fact: %w", err)
		}
	}

	if inserted {
		if _, err := tx.Exec(ctx, `
			INSERT INTO user_fact_counters (user_id, fact_key, variant, total, last_occurred_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,now())
			ON CONFLICT (user_id, fact_key, variant) DO UPDATE
			SET total = user_fact_counters.total + EXCLUDED.total,
			    last_occurred_at = GREATEST(user_fact_counters.last_occurred_at, EXCLUDED.last_occurred_at),
			    updated_at = now()`,
			input.UserID, input.FactKey, input.Variant, input.Delta, occurredAt); err != nil {
			return FactResult{}, fmt.Errorf("bump fact counter: %w", err)
		}
	}

	var total int64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(total, 0) FROM user_fact_counters
		WHERE user_id = $1 AND fact_key = $2 AND variant = $3`,
		input.UserID, input.FactKey, input.Variant).Scan(&total); err != nil {
		return FactResult{}, fmt.Errorf("read fact counter: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return FactResult{}, fmt.Errorf("commit fact: %w", err)
	}
	return FactResult{Inserted: inserted, Total: total}, nil
}
