package entitymeta

import (
	"context"
	"fmt"
	"strings"
)

type ValueGuardField struct {
	FieldKey   string
	Visibility string
	Enabled    bool
}

type ValueGuardSubject struct {
	EntityType  string
	EntityID    int64
	OwnerUserID int64
	Exists      bool
	Fields      map[string]ValueGuardField
}

// LoadValueGuardSubject 读取 entity-meta value 路由的当前实体所有权与字段保护等级。
// 每次请求读取 PostgreSQL，确保另一 API 节点更新 owner/visibility 后立即生效。
// topics.author_user_id 可空（作者注销后 FK 置 NULL），归一成 0 表示「作者已注销」。
func (s *PostgresStore) LoadValueGuardSubject(
	ctx context.Context,
	entityType string,
	entityID int64,
	fieldKeys []string,
) (ValueGuardSubject, error) {
	entityType = strings.TrimSpace(entityType)
	if s == nil || s.pool == nil || ctx == nil || entityID <= 0 || !validEntityType(entityType) {
		return ValueGuardSubject{}, ErrEntityNotFound
	}
	subject := ValueGuardSubject{EntityType: entityType, EntityID: entityID, Fields: map[string]ValueGuardField{}}
	var err error
	switch entityType {
	case EntityUser:
		err = s.pool.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, entityID).Scan(&subject.OwnerUserID)
	case EntityTopic:
		err = s.pool.QueryRow(ctx, `SELECT COALESCE(author_user_id, 0) FROM topics WHERE id = $1`, entityID).Scan(&subject.OwnerUserID)
	}
	if err != nil {
		return ValueGuardSubject{}, fmt.Errorf("load entity-meta guard owner: %w", err)
	}
	subject.Exists = true
	if len(fieldKeys) == 0 {
		return subject, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT field_key, visibility, enabled
		FROM entity_field_definitions
		WHERE entity_type = $1 AND field_key = ANY($2::text[])
	`, entityType, fieldKeys)
	if err != nil {
		return ValueGuardSubject{}, fmt.Errorf("load entity-meta guard fields: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var field ValueGuardField
		if err := rows.Scan(&field.FieldKey, &field.Visibility, &field.Enabled); err != nil {
			return ValueGuardSubject{}, err
		}
		subject.Fields[field.FieldKey] = field
	}
	if err := rows.Err(); err != nil {
		return ValueGuardSubject{}, err
	}
	return subject, nil
}
