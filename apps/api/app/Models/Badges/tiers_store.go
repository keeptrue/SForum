package badges

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// 等级表（badge_tiers）的 Postgres 读写。
//
// 单独成文件：postgres_store.go 已 435 行，且那边的每条 SQL 都围绕徽章目录；
// 等级表是**另一张表、另一套写规则**（删除要先迁移引用），混进去只会让两边都变难读。

// tierColumnsSQL 是等级行的列集合；ListTiers / GetTier 共用，保证两处形状一致。
const tierColumnsSQL = `
	t.key, t.name_zh, t.name_en, t.icon, t.color, t.position, t.enabled,
	COALESCE(counts.badge_count, 0) AS badge_count, t.created_at, t.updated_at`

// tierCountsJoinSQL 把「引用该等级的徽章数」聚合进来。
//
// 删除确认弹窗必须先知道这个数字：>0 时删除需要 migrateTo，运营不该在收到 409
// 之后才发现有 30 个徽章挂在上面。
const tierCountsJoinSQL = `
	FROM badge_tiers AS t
	LEFT JOIN (
		SELECT tier, COUNT(*) AS badge_count FROM badges GROUP BY tier
	) AS counts ON counts.tier = t.key`

func scanTier(row pgx.Row) (TierDef, error) {
	var tier TierDef
	if err := row.Scan(&tier.Key, &tier.NameZh, &tier.NameEn, &tier.Icon, &tier.Color,
		&tier.Position, &tier.Enabled, &tier.BadgeCount, &tier.CreatedAt, &tier.UpdatedAt); err != nil {
		return TierDef{}, err
	}
	return tier, nil
}

// ListTiers 返回全部等级（含停用），按 position 升序。
func (s *PostgresStore) ListTiers(ctx context.Context) ([]TierDef, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+tierColumnsSQL+tierCountsJoinSQL+`
		ORDER BY t.position, t.key`)
	if err != nil {
		return nil, fmt.Errorf("list badge tiers: %w", err)
	}
	defer rows.Close()
	items := make([]TierDef, 0, 8)
	for rows.Next() {
		tier, err := scanTier(rows)
		if err != nil {
			return nil, fmt.Errorf("scan badge tier: %w", err)
		}
		items = append(items, tier)
	}
	return items, rows.Err()
}

// GetTier 按 key 读单个等级。
func (s *PostgresStore) GetTier(ctx context.Context, key string) (TierDef, error) {
	tier, err := scanTier(s.pool.QueryRow(ctx, `SELECT `+tierColumnsSQL+tierCountsJoinSQL+`
		WHERE t.key = $1`, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return TierDef{}, ErrBadgeTierNotFound
	}
	if err != nil {
		return TierDef{}, fmt.Errorf("get badge tier: %w", err)
	}
	return tier, nil
}

// CreateTier 插入等级。key 冲突返回 ErrBadgeTierKeyTaken。
//
// position 为 TierPositionAppend（或任何负数哨兵）时取 max(position)+1：position 0 是
// **最高**位次，若把「调用方没传」当成 0，新等级会与金牌并列最高，挂在它上面的徽章
// 会和金牌徽章争同一个名次。查询与插入放在同一条语句里，避免两个并发创建拿到同一个
// max+1。
func (s *PostgresStore) CreateTier(ctx context.Context, input TierInput) (TierDef, error) {
	var key string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO badge_tiers (key, name_zh, name_en, icon, color, position, enabled)
		SELECT $1, $2, $3, $4, $5,
		       CASE WHEN $6 < 0
		            THEN COALESCE((SELECT MAX(position) FROM badge_tiers), -1) + 1
		            ELSE $6 END,
		       $7
		RETURNING key`,
		input.Key, input.NameZh, input.NameEn, input.Icon, input.Color, input.Position, input.Enabled,
	).Scan(&key)
	if err != nil {
		if isUniqueViolation(err) {
			return TierDef{}, ErrBadgeTierKeyTaken
		}
		return TierDef{}, fmt.Errorf("create badge tier: %w", err)
	}
	return s.GetTier(ctx, key)
}

// UpdateTier 更新等级的展示字段；key 不可变。
//
// 停用一档会让引用它的徽章退出「最高级徽章」竞争（topBadgeOrderSQL 的 t.enabled），
// 所以「停用最后一个启用档」等价于把全站 top_badge_id 清空 —— 必须与删除同口径拦在
// 写入口：至少保留一个启用档。
//
// 锁行 + 计数在同一事务里：锁住被改的那一行，避免「同一档被两个请求同时停用」
// 时双方都基于旧状态判断。跨档并发（两个请求各停一档）仍有窄窗口，见 Known
// Residuals——这里防的是运营误点，不是对抗性并发。
func (s *PostgresStore) UpdateTier(ctx context.Context, key string, input TierInput) (TierDef, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TierDef{}, fmt.Errorf("begin update badge tier: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT TRUE FROM badge_tiers WHERE key = $1 FOR UPDATE`, key).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TierDef{}, ErrBadgeTierNotFound
		}
		return TierDef{}, fmt.Errorf("lock badge tier: %w", err)
	}

	if !input.Enabled {
		var remainingEnabled int
		if err := tx.QueryRow(ctx, `
			SELECT COUNT(*) FROM badge_tiers WHERE enabled AND key <> $1`, key).Scan(&remainingEnabled); err != nil {
			return TierDef{}, fmt.Errorf("count remaining enabled tiers: %w", err)
		}
		if remainingEnabled == 0 {
			return TierDef{}, fmt.Errorf("%w: at least one enabled tier must remain", ErrBadgeTierInUse)
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE badge_tiers
		SET name_zh = $2, name_en = $3, icon = $4, color = $5, position = $6,
		    enabled = $7, updated_at = now()
		WHERE key = $1`,
		key, input.NameZh, input.NameEn, input.Icon, input.Color, input.Position, input.Enabled); err != nil {
		return TierDef{}, fmt.Errorf("update badge tier: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return TierDef{}, fmt.Errorf("commit update badge tier: %w", err)
	}
	return s.GetTier(ctx, key)
}

// DeleteTier 在**单事务**里把引用该等级的徽章迁到 migrateTo，再删除等级行。
//
// 三步顺序不可交换：先校验目标（存在/非自身/启用/不是最后一档）→ 改引用 →
// 删行。外键是 ON DELETE RESTRICT，所以任何一步漏掉都会在最后一步失败并整体回滚，
// 不会留下「一半徽章指向已删等级」的中间态。
//
// 为什么必须显式给 migrateTo：静默级联会把 30 个徽章悄悄降成铜牌，而运营在
// 删除确认框里看不到这件事。要求选择目标等于把决定权交回给做出删除的人。
func (s *PostgresStore) DeleteTier(ctx context.Context, key string, migrateTo string) error {
	if migrateTo == "" {
		return fmt.Errorf("%w: migrateTo is required", ErrBadgeTierInvalid)
	}
	if migrateTo == key {
		return fmt.Errorf("%w: migrateTo must differ from the deleted tier", ErrBadgeTierInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete badge tier: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// FOR UPDATE 锁住被删行与目标行：并发删除两档时，两边都读到「还有另一档启用」，
	// 提交后却可能一档都不剩。锁住两行能让第二个事务看到第一个的结果。
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT TRUE FROM badge_tiers WHERE key = $1 FOR UPDATE`, key).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrBadgeTierNotFound
		}
		return fmt.Errorf("lock badge tier: %w", err)
	}
	var targetEnabled bool
	if err := tx.QueryRow(ctx, `SELECT enabled FROM badge_tiers WHERE key = $1 FOR UPDATE`, migrateTo).Scan(&targetEnabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: migrateTo %q does not exist", ErrBadgeTierNotFound, migrateTo)
		}
		return fmt.Errorf("lock migrate target tier: %w", err)
	}
	if !targetEnabled {
		return fmt.Errorf("%w: migrateTo %q is disabled", ErrBadgeTierInvalid, migrateTo)
	}

	// 「最后一个启用档」保护：删掉它会让 topBadgeOrderSQL 的 JOIN 一行为空，
	// 全站所有人的最高级徽章同时消失——这种影响面不该由一次误点触发。
	var remainingEnabled int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM badge_tiers WHERE enabled AND key <> $1`, key).Scan(&remainingEnabled); err != nil {
		return fmt.Errorf("count remaining enabled tiers: %w", err)
	}
	if remainingEnabled == 0 {
		return fmt.Errorf("%w: at least one enabled tier must remain", ErrBadgeTierInUse)
	}

	if _, err := tx.Exec(ctx, `UPDATE badges SET tier = $2, updated_at = now() WHERE tier = $1`, key, migrateTo); err != nil {
		return fmt.Errorf("migrate badges to target tier: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM badge_tiers WHERE key = $1`, key); err != nil {
		return fmt.Errorf("delete badge tier: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete badge tier: %w", err)
	}
	return nil
}

// ReorderTiers 按 keys 的顺序重写 position；未列出的等级按原顺序追加在后。
//
// 为什么允许「只提交一部分 key」：后台拖拽一次只会带上它看得见的那几档（可能有
// 分页或过滤）。把未列出的档位排在后面，比拒绝整次请求更符合运营预期——
// 被隐藏的档位不该阻止可见档位的排序。
func (s *PostgresStore) ReorderTiers(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("%w: keys are required", ErrBadgeTierInvalid)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder badge tiers: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT key FROM badge_tiers ORDER BY position, key FOR UPDATE`)
	if err != nil {
		return fmt.Errorf("lock badge tiers: %w", err)
	}
	existing := make([]string, 0, 8)
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return fmt.Errorf("scan badge tier key: %w", err)
		}
		existing = append(existing, key)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	known := make(map[string]struct{}, len(existing))
	for _, key := range existing {
		known[key] = struct{}{}
	}
	seen := make(map[string]struct{}, len(keys))
	order := make([]string, 0, len(existing))
	for _, key := range keys {
		if _, ok := known[key]; !ok {
			return fmt.Errorf("%w: unknown tier %q", ErrBadgeTierNotFound, key)
		}
		if _, dup := seen[key]; dup {
			return fmt.Errorf("%w: duplicate tier %q", ErrBadgeTierInvalid, key)
		}
		seen[key] = struct{}{}
		order = append(order, key)
	}
	for _, key := range existing {
		if _, listed := seen[key]; !listed {
			order = append(order, key)
		}
	}
	for index, key := range order {
		if _, err := tx.Exec(ctx, `
			UPDATE badge_tiers SET position = $2, updated_at = now() WHERE key = $1`, key, index); err != nil {
			return fmt.Errorf("set badge tier position: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit reorder badge tiers: %w", err)
	}
	return nil
}
