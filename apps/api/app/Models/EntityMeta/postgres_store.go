package entitymeta

import (
	"context"
	"errors"
	"strings"
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

func (s *PostgresStore) ListDefinitions(ctx context.Context, entityType string) ([]fieldRow, error) {
	entityType = strings.TrimSpace(entityType)
	var (
		rows pgx.Rows
		err  error
	)
	if entityType == "" {
		rows, err = s.pool.Query(ctx, `
SELECT id, field_key, entity_type, value_type, visibility,
       label_zh_cn, label_en_us, description_zh_cn, description_en_us,
       owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at
FROM entity_field_definitions
ORDER BY entity_type ASC, sort_order ASC, field_key ASC`)
	} else {
		rows, err = s.pool.Query(ctx, `
SELECT id, field_key, entity_type, value_type, visibility,
       label_zh_cn, label_en_us, description_zh_cn, description_en_us,
       owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at
FROM entity_field_definitions
WHERE entity_type = $1
ORDER BY sort_order ASC, field_key ASC`, entityType)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFieldRows(rows)
}

func (s *PostgresStore) GetDefinitionByKey(ctx context.Context, fieldKey string) (fieldRow, error) {
	row := s.pool.QueryRow(ctx, `
SELECT id, field_key, entity_type, value_type, visibility,
       label_zh_cn, label_en_us, description_zh_cn, description_en_us,
       owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at
FROM entity_field_definitions WHERE field_key = $1`, fieldKey)
	item, err := scanFieldRow(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldRow{}, ErrNotFound
	}
	return item, err
}

func (s *PostgresStore) CreateDefinition(ctx context.Context, row fieldRow) (fieldRow, error) {
	if len(row.Constraints) == 0 {
		row.Constraints = []byte("{}")
	}
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
INSERT INTO entity_field_definitions (
  field_key, entity_type, value_type, visibility,
  label_zh_cn, label_en_us, description_zh_cn, description_en_us,
  owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at
) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13::jsonb,$14,$14)
RETURNING id, field_key, entity_type, value_type, visibility,
          label_zh_cn, label_en_us, description_zh_cn, description_en_us,
          owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at`,
		row.FieldKey, row.EntityType, row.ValueType, row.Visibility,
		row.LabelZHCN, row.LabelENUS, row.DescriptionZHCN, row.DescriptionENUS,
		row.OwnerExtensionID, row.Required, row.Enabled, row.SortOrder, string(row.Constraints), now,
	).Scan(
		&row.ID, &row.FieldKey, &row.EntityType, &row.ValueType, &row.Visibility,
		&row.LabelZHCN, &row.LabelENUS, &row.DescriptionZHCN, &row.DescriptionENUS,
		&row.OwnerExtensionID, &row.Required, &row.Enabled, &row.SortOrder, &row.Constraints,
		&row.CreatedAt, &row.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return fieldRow{}, ErrInvalid
		}
		return fieldRow{}, err
	}
	return row, nil
}

func (s *PostgresStore) UpdateDefinition(ctx context.Context, fieldKey string, row fieldRow) (fieldRow, error) {
	if len(row.Constraints) == 0 {
		row.Constraints = []byte("{}")
	}
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
UPDATE entity_field_definitions SET
  visibility = $2,
  label_zh_cn = $3, label_en_us = $4,
  description_zh_cn = $5, description_en_us = $6,
  owner_extension_id = $7, required = $8, enabled = $9, sort_order = $10,
  constraints = $11::jsonb, updated_at = $12
WHERE field_key = $1
RETURNING id, field_key, entity_type, value_type, visibility,
          label_zh_cn, label_en_us, description_zh_cn, description_en_us,
          owner_extension_id, required, enabled, sort_order, constraints, created_at, updated_at`,
		fieldKey, row.Visibility, row.LabelZHCN, row.LabelENUS,
		row.DescriptionZHCN, row.DescriptionENUS, row.OwnerExtensionID,
		row.Required, row.Enabled, row.SortOrder, string(row.Constraints), now,
	).Scan(
		&row.ID, &row.FieldKey, &row.EntityType, &row.ValueType, &row.Visibility,
		&row.LabelZHCN, &row.LabelENUS, &row.DescriptionZHCN, &row.DescriptionENUS,
		&row.OwnerExtensionID, &row.Required, &row.Enabled, &row.SortOrder, &row.Constraints,
		&row.CreatedAt, &row.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return fieldRow{}, ErrNotFound
	}
	return row, err
}

func (s *PostgresStore) DeleteDefinition(ctx context.Context, fieldKey string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM entity_field_definitions WHERE field_key = $1`, fieldKey)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) ListValues(ctx context.Context, entityType string, entityID int64) ([]valueRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT entity_type, entity_id, field_key, value_text, updated_at, updated_by_user_id
FROM entity_meta_values
WHERE entity_type = $1 AND entity_id = $2
ORDER BY field_key ASC`, entityType, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []valueRow
	for rows.Next() {
		var item valueRow
		if err := rows.Scan(&item.EntityType, &item.EntityID, &item.FieldKey, &item.ValueText, &item.UpdatedAt, &item.UpdatedByUserID); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *PostgresStore) UpsertValue(ctx context.Context, row valueRow) (valueRow, error) {
	now := time.Now().UTC()
	err := s.pool.QueryRow(ctx, `
INSERT INTO entity_meta_values (entity_type, entity_id, field_key, value_text, updated_at, updated_by_user_id)
VALUES ($1,$2,$3,$4,$5,$6)
ON CONFLICT (entity_type, entity_id, field_key) DO UPDATE SET
  value_text = EXCLUDED.value_text,
  updated_at = EXCLUDED.updated_at,
  updated_by_user_id = EXCLUDED.updated_by_user_id
RETURNING entity_type, entity_id, field_key, value_text, updated_at, updated_by_user_id`,
		row.EntityType, row.EntityID, row.FieldKey, row.ValueText, now, row.UpdatedByUserID,
	).Scan(&row.EntityType, &row.EntityID, &row.FieldKey, &row.ValueText, &row.UpdatedAt, &row.UpdatedByUserID)
	return row, err
}

func (s *PostgresStore) DeleteValue(ctx context.Context, entityType string, entityID int64, fieldKey string) error {
	_, err := s.pool.Exec(ctx, `
DELETE FROM entity_meta_values
WHERE entity_type = $1 AND entity_id = $2 AND field_key = $3`, entityType, entityID, fieldKey)
	return err
}

func (s *PostgresStore) EntityExists(ctx context.Context, entityType string, entityID int64) (bool, error) {
	var exists bool
	var err error
	switch entityType {
	case EntityUser:
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, entityID).Scan(&exists)
	case EntityTopic:
		err = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM topics WHERE id = $1)`, entityID).Scan(&exists)
	default:
		return false, ErrInvalid
	}
	return exists, err
}

// TopicAuthorID 返回 topic 的所有者。作者被带外删除后 author_user_id 由 FK
// 置为 NULL（ON DELETE SET NULL），这里归一成 0：调用方拿到 (0, true) 表示
// 「记录在、作者已注销」，而不是把它当成扫描失败。
func (s *PostgresStore) TopicAuthorID(ctx context.Context, topicID int64) (int64, bool, error) {
	var authorID int64
	err := s.pool.QueryRow(ctx, `SELECT COALESCE(author_user_id, 0) FROM topics WHERE id = $1`, topicID).Scan(&authorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return authorID, true, nil
}

func scanFieldRows(rows pgx.Rows) ([]fieldRow, error) {
	var out []fieldRow
	for rows.Next() {
		item, err := scanFieldRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

type scannable interface {
	Scan(dest ...any) error
}

func scanFieldRow(row scannable) (fieldRow, error) {
	var item fieldRow
	err := row.Scan(
		&item.ID, &item.FieldKey, &item.EntityType, &item.ValueType, &item.Visibility,
		&item.LabelZHCN, &item.LabelENUS, &item.DescriptionZHCN, &item.DescriptionENUS,
		&item.OwnerExtensionID, &item.Required, &item.Enabled, &item.SortOrder, &item.Constraints,
		&item.CreatedAt, &item.UpdatedAt,
	)
	return item, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate key")
}
