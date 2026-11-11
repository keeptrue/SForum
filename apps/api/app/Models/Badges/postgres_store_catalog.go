package badges

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// 目录写路径（徽章与分组 CRUD）。
//
// 与读路径分文件：写路径要处理唯一冲突、存在性、以及「更新后重算」的调用契约，
// 混在同一个文件里会让 postgres_store.go 很快越过 500 行的可读边界。

// CreateBadge 插入徽章定义。slug 冲突返回 ErrBadgeSlugTaken。
func (s *PostgresStore) CreateBadge(ctx context.Context, input BadgeInput) (Badge, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO badges (slug, name, description, group_id, tier, icon, grant_mode,
		                    metric, threshold, enabled, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		input.Slug, input.Name, input.Description, input.GroupID, input.Tier, input.Icon,
		input.GrantMode, nullableMetric(input.Metric), input.Threshold, input.Enabled, input.Position,
	).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return Badge{}, ErrBadgeSlugTaken
		}
		return Badge{}, fmt.Errorf("create badge: %w", err)
	}
	return s.GetBadge(ctx, id)
}

// UpdateBadge 全量更新徽章定义。
func (s *PostgresStore) UpdateBadge(ctx context.Context, badgeID int64, input BadgeInput) (Badge, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE badges
		SET slug = $2, name = $3, description = $4, group_id = $5, tier = $6,
		    icon = $7, grant_mode = $8, metric = $9, threshold = $10,
		    enabled = $11, position = $12, updated_at = now()
		WHERE id = $1`,
		badgeID, input.Slug, input.Name, input.Description, input.GroupID, input.Tier,
		input.Icon, input.GrantMode, nullableMetric(input.Metric), input.Threshold,
		input.Enabled, input.Position,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Badge{}, ErrBadgeSlugTaken
		}
		return Badge{}, fmt.Errorf("update badge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return Badge{}, ErrBadgeNotFound
	}
	return s.GetBadge(ctx, badgeID)
}

// DeleteBadge 删除徽章；user_badges 级联删，users.top_badge_id 由外键置空。
func (s *PostgresStore) DeleteBadge(ctx context.Context, badgeID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM badges WHERE id = $1`, badgeID)
	if err != nil {
		return fmt.Errorf("delete badge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBadgeNotFound
	}
	return nil
}

// CreateGroup 插入分组。key 冲突返回 ErrBadgeSlugTaken。
func (s *PostgresStore) CreateGroup(ctx context.Context, input BadgeGroupInput) (BadgeGroup, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO badge_groups (key, name, description, position)
		VALUES ($1, $2, $3, $4)
		RETURNING id`, input.Key, input.Name, input.Description, input.Position).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return BadgeGroup{}, ErrBadgeSlugTaken
		}
		return BadgeGroup{}, fmt.Errorf("create badge group: %w", err)
	}
	return s.getGroup(ctx, id)
}

// UpdateGroup 更新分组。
func (s *PostgresStore) UpdateGroup(ctx context.Context, groupID int64, input BadgeGroupInput) (BadgeGroup, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE badge_groups
		SET key = $2, name = $3, description = $4, position = $5, updated_at = now()
		WHERE id = $1`, groupID, input.Key, input.Name, input.Description, input.Position)
	if err != nil {
		if isUniqueViolation(err) {
			return BadgeGroup{}, ErrBadgeSlugTaken
		}
		return BadgeGroup{}, fmt.Errorf("update badge group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return BadgeGroup{}, ErrBadgeNotFound
	}
	return s.getGroup(ctx, groupID)
}

// DeleteGroup 删除分组；组内徽章的 group_id 由外键 SET NULL 落成未分组。
func (s *PostgresStore) DeleteGroup(ctx context.Context, groupID int64) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM badge_groups WHERE id = $1`, groupID)
	if err != nil {
		return fmt.Errorf("delete badge group: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrBadgeNotFound
	}
	return nil
}

func (s *PostgresStore) getGroup(ctx context.Context, groupID int64) (BadgeGroup, error) {
	var group BadgeGroup
	err := s.pool.QueryRow(ctx, `
		SELECT badge_groups.id, badge_groups.key, badge_groups.name, badge_groups.description,
		       badge_groups.position, COALESCE(counts.badge_count, 0),
		       badge_groups.created_at, badge_groups.updated_at
		FROM badge_groups
		LEFT JOIN (
			SELECT group_id, COUNT(*) AS badge_count FROM badges GROUP BY group_id
		) AS counts ON counts.group_id = badge_groups.id
		WHERE badge_groups.id = $1`, groupID).Scan(
		&group.ID, &group.Key, &group.Name, &group.Description,
		&group.Position, &group.BadgeCount, &group.CreatedAt, &group.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return BadgeGroup{}, ErrBadgeNotFound
	}
	if err != nil {
		return BadgeGroup{}, fmt.Errorf("get badge group: %w", err)
	}
	return group, nil
}

// nullableMetric 把手动徽章的 nil 指标写成 NULL；带值时必须是白名单枚举。
func nullableMetric(metric *ActivityMetric) any {
	if metric == nil {
		return nil
	}
	return string(*metric)
}

// isUniqueViolation 识别 PostgreSQL 23505。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
