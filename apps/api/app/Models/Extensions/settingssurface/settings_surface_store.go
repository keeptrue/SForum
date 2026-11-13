package settingssurface

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrInvalid 表示覆盖键不完整（空扩展/点/贡献 id）。
	ErrInvalid = errors.New("settings_surface: invalid")
	// ErrNotFound 表示删除时该覆盖行已不存在。
	ErrNotFound = errors.New("settings_surface: override not found")
)

// PostgresStore 持久化治理覆盖行。
//
// 只存覆盖项：Upsert 建/改一行，Delete 恢复 manifest 默认。没有任何
// 「把整份声明投影进表」的路径——那样一次插件升级就会覆盖运营的选择。
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

const overrideSelectSQL = `
SELECT extension_id, point, contribution_id, item_key, enabled, sort_order,
       updated_by_user_id, created_at, updated_at
FROM settings_surface_items`

func (s *PostgresStore) ListOverrides(ctx context.Context) ([]Override, error) {
	rows, err := s.pool.Query(ctx, overrideSelectSQL+`
	ORDER BY point ASC, extension_id ASC, contribution_id ASC, item_key ASC`)
	if err != nil {
		return nil, fmt.Errorf("list settings surface overrides: %w", err)
	}
	defer rows.Close()
	overrides := []Override{}
	for rows.Next() {
		item, err := scanOverride(rows)
		if err != nil {
			return nil, err
		}
		overrides = append(overrides, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settings surface overrides: %w", err)
	}
	return overrides, nil
}

func (s *PostgresStore) UpsertOverride(ctx context.Context, input OverrideInput) (Override, error) {
	key := input.Key.Normalized()
	if !key.valid() {
		return Override{}, ErrInvalid
	}
	row := s.pool.QueryRow(ctx, `
INSERT INTO settings_surface_items (
  extension_id, point, contribution_id, item_key, enabled, sort_order, updated_by_user_id
) VALUES ($1,$2,$3,$4,$5,$6,$7)
ON CONFLICT (extension_id, point, contribution_id, item_key) DO UPDATE SET
  enabled = EXCLUDED.enabled,
  sort_order = EXCLUDED.sort_order,
  updated_by_user_id = EXCLUDED.updated_by_user_id,
  updated_at = now()
RETURNING extension_id, point, contribution_id, item_key, enabled, sort_order,
          updated_by_user_id, created_at, updated_at`,
		key.ExtensionID, key.Point, key.ContributionID, key.ItemKey,
		input.Enabled, input.SortOrder, input.UpdatedByUserID,
	)
	item, err := scanOverride(row)
	if err != nil {
		return Override{}, fmt.Errorf("upsert settings surface override: %w", err)
	}
	return item, nil
}

func (s *PostgresStore) DeleteOverride(ctx context.Context, key OverrideKey) error {
	key = key.Normalized()
	if !key.valid() {
		return ErrInvalid
	}
	tag, err := s.pool.Exec(ctx, `
DELETE FROM settings_surface_items
WHERE extension_id = $1 AND point = $2 AND contribution_id = $3 AND item_key = $4`,
		key.ExtensionID, key.Point, key.ContributionID, key.ItemKey)
	if err != nil {
		return fmt.Errorf("delete settings surface override: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

type scannable interface {
	Scan(dest ...any) error
}

func scanOverride(row scannable) (Override, error) {
	var (
		item    Override
		updated *int64
	)
	if err := row.Scan(
		&item.Key.ExtensionID, &item.Key.Point, &item.Key.ContributionID, &item.Key.ItemKey,
		&item.Enabled, &item.SortOrder, &updated, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Override{}, ErrNotFound
		}
		return Override{}, err
	}
	item.UpdatedByUserID = updated
	return item, nil
}

// OverrideIndex 把覆盖行按归一化键索引，供 Provider 逐条应用。
func OverrideIndex(overrides []Override) map[OverrideKey]Override {
	index := make(map[OverrideKey]Override, len(overrides))
	for _, item := range overrides {
		index[item.Key.Normalized()] = item
	}
	return index
}

// EffectiveOrder 返回治理覆盖顺序（有覆盖时）或声明顺序。
func EffectiveOrder(key OverrideKey, declared int, index map[OverrideKey]Override) int {
	if item, ok := index[key.Normalized()]; ok && item.SortOrder != nil {
		return *item.SortOrder
	}
	return declared
}
