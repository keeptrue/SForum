package badges

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresStore 是 Store 的生产实现。
type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

// topBadgeOrderSQL 是「最高级徽章」的唯一排序定义。
//
// 排序键来自等级表：badge_tiers.position 升序（0 = 最高档）→ badges.position →
// granted_at 降序。**不再**用 CASE 映射 gold/silver/bronze——等级是运营可增删的
// 数据，写死映射会让新档位永远排最后。
//
// 使用本常量的每个查询都**必须**自带
// `JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled`：
//   - JOIN 而不是 LEFT JOIN：停用档位的徽章不该参与竞争（等价于「这一档不存在」）；
//   - 别名固定为 t，本常量引用 t.position。
//
// 资料页、帖子旁 chip、top_badge_id 维护必须共用这一段，否则同一个人在两个地方
// 会显示不同的徽章。
const topBadgeOrderSQL = `
	t.position ASC,
	badges.position ASC,
	user_badges.granted_at DESC`

// badgeTierColumnsSQL 是徽章的等级外观列（双语名/颜色/位次）。
//
// LEFT JOIN + COALESCE 而不是 INNER JOIN：外键保证正常情况一定有行，但读路径
// 不该因为「等级行暂时缺失」（运维手工删数据、回滚中的中间态）把整个后台列表变成 500。
// 空外观在 DTO 层是「没有等级信息」，前端有兜底分支。
const badgeTierColumnsSQL = `
	COALESCE(tier_def.name_zh, '') AS tier_name_zh,
	COALESCE(tier_def.name_en, '') AS tier_name_en,
	COALESCE(tier_def.color, '') AS tier_color,
	COALESCE(tier_def.position, 2147483647) AS tier_position`

const badgeColumnsSQL = `
	badges.id, badges.slug, badges.name, badges.description,
	badges.group_id, badge_groups.name AS group_name, badge_groups.key AS group_key,
	badges.tier, badges.icon, badges.grant_mode, badges.metric, badges.threshold,
	badges.enabled, badges.position, badges.created_at, badges.updated_at,
	COALESCE(grants.granted_count, 0) AS granted_count,` + badgeTierColumnsSQL

const badgeJoinsSQL = `
	FROM badges
	LEFT JOIN badge_groups ON badge_groups.id = badges.group_id
	LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
	LEFT JOIN (
		SELECT badge_id, COUNT(*) AS granted_count FROM user_badges GROUP BY badge_id
	) AS grants ON grants.badge_id = badges.id`

func scanBadge(row pgx.Row) (Badge, error) {
	var badge Badge
	var metric *string
	if err := row.Scan(
		&badge.ID, &badge.Slug, &badge.Name, &badge.Description,
		&badge.GroupID, &badge.GroupName, &badge.GroupKey,
		&badge.Tier, &badge.Icon, &badge.GrantMode, &metric, &badge.Threshold,
		&badge.Enabled, &badge.Position, &badge.CreatedAt, &badge.UpdatedAt,
		&badge.GrantedCount,
		&badge.Appearance.NameZh, &badge.Appearance.NameEn, &badge.Appearance.Color,
		&badge.Appearance.Position,
	); err != nil {
		return Badge{}, err
	}
	if metric != nil {
		value := ActivityMetric(*metric)
		badge.Metric = &value
	}
	return badge, nil
}

// ListBadges 返回全部徽章（含停用），按分组位置、徽章位置排序。
func (s *PostgresStore) ListBadges(ctx context.Context) ([]Badge, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+`
		ORDER BY COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST, badges.position, badges.id`)
	if err != nil {
		return nil, fmt.Errorf("list badges: %w", err)
	}
	defer rows.Close()
	items := make([]Badge, 0, 16)
	for rows.Next() {
		badge, err := scanBadge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan badge: %w", err)
		}
		items = append(items, badge)
	}
	return items, rows.Err()
}

// GetBadge 按 id 读单个徽章。
func (s *PostgresStore) GetBadge(ctx context.Context, badgeID int64) (Badge, error) {
	badge, err := scanBadge(s.pool.QueryRow(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+` WHERE badges.id = $1`, badgeID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Badge{}, ErrBadgeNotFound
	}
	if err != nil {
		return Badge{}, fmt.Errorf("get badge: %w", err)
	}
	return badge, nil
}

// GetBadgeBySlug 按 slug 读单个徽章。
func (s *PostgresStore) GetBadgeBySlug(ctx context.Context, slug string) (Badge, error) {
	badge, err := scanBadge(s.pool.QueryRow(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+` WHERE badges.slug = $1`, slug))
	if errors.Is(err, pgx.ErrNoRows) {
		return Badge{}, ErrBadgeNotFound
	}
	if err != nil {
		return Badge{}, fmt.Errorf("get badge by slug: %w", err)
	}
	return badge, nil
}

// ListGroups 返回全部分组（含组内徽章数）。
func (s *PostgresStore) ListGroups(ctx context.Context) ([]BadgeGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT badge_groups.id, badge_groups.key, badge_groups.name, badge_groups.description,
		       badge_groups.position, COALESCE(counts.badge_count, 0),
		       badge_groups.created_at, badge_groups.updated_at
		FROM badge_groups
		LEFT JOIN (
			SELECT group_id, COUNT(*) AS badge_count FROM badges GROUP BY group_id
		) AS counts ON counts.group_id = badge_groups.id
		ORDER BY badge_groups.position, badge_groups.id`)
	if err != nil {
		return nil, fmt.Errorf("list badge groups: %w", err)
	}
	defer rows.Close()
	items := make([]BadgeGroup, 0, 8)
	for rows.Next() {
		var group BadgeGroup
		if err := rows.Scan(&group.ID, &group.Key, &group.Name, &group.Description,
			&group.Position, &group.BadgeCount, &group.CreatedAt, &group.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan badge group: %w", err)
		}
		items = append(items, group)
	}
	return items, rows.Err()
}

// ListUserBadges 返回「全部启用徽章 × 该用户」的墙：未获得的也要出现（进度用）。
func (s *PostgresStore) ListUserBadges(ctx context.Context, userID int64) ([]UserBadge, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT badges.id, badges.slug, badges.name, badges.description,
		       badge_groups.key AS group_key, badges.tier, badges.icon,
		       badges.grant_mode, badges.metric, badges.threshold, badges.position,
		       user_badges.granted_at,
		       `+badgeTierColumnsSQL+`
		FROM badges
		LEFT JOIN badge_groups ON badge_groups.id = badges.group_id
		LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
		LEFT JOIN user_badges ON user_badges.badge_id = badges.id AND user_badges.user_id = $1
		WHERE badges.enabled
		ORDER BY COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST,
		         badges.position, badges.id`, userID)
	if err != nil {
		return nil, fmt.Errorf("list user badges: %w", err)
	}
	defer rows.Close()
	items := make([]UserBadge, 0, 16)
	for rows.Next() {
		var item UserBadge
		var metric *string
		if err := rows.Scan(
			&item.Badge.ID, &item.Badge.Slug, &item.Badge.Name, &item.Badge.Description,
			&item.Badge.GroupKey, &item.Badge.Tier, &item.Badge.Icon,
			&item.Badge.GrantMode, &metric, &item.Badge.Threshold, &item.Badge.Position,
			&item.GrantedAt,
			&item.Badge.Appearance.NameZh, &item.Badge.Appearance.NameEn,
			&item.Badge.Appearance.Color, &item.Badge.Appearance.Position,
		); err != nil {
			return nil, fmt.Errorf("scan user badge: %w", err)
		}
		if metric != nil {
			value := ActivityMetric(*metric)
			item.Badge.Metric = &value
		}
		item.Earned = item.GrantedAt != nil
		if item.Badge.Threshold != nil {
			item.Progress.Threshold = *item.Badge.Threshold
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// TopBadgesFor 一次查询取整页用户的最高级徽章，避免列表 N+1。
//
// 只返回「启用」徽章：停用徽章的已授予行保留，但不再展示。
func (s *PostgresStore) TopBadgesFor(ctx context.Context, userIDs []int64) (map[int64]BadgeRef, error) {
	result := make(map[int64]BadgeRef, len(userIDs))
	if len(userIDs) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT users.id, badges.slug, badges.name, badges.tier, badges.icon,
		       `+badgeTierColumnsSQL+`
		FROM users
		JOIN badges ON badges.id = users.top_badge_id
		LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
		WHERE users.id = ANY($1) AND badges.enabled`, userIDs)
	if err != nil {
		return nil, fmt.Errorf("top badges for users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var userID int64
		var ref BadgeRef
		if err := rows.Scan(&userID, &ref.Slug, &ref.Name, &ref.Tier, &ref.Icon,
			&ref.Appearance.NameZh, &ref.Appearance.NameEn,
			&ref.Appearance.Color, &ref.Appearance.Position); err != nil {
			return nil, fmt.Errorf("scan top badge: %w", err)
		}
		result[userID] = ref
	}
	return result, rows.Err()
}

// ListEnabledAutomaticBadges 返回启用中的自动徽章（规则评估的输入）。
func (s *PostgresStore) ListEnabledAutomaticBadges(ctx context.Context) ([]Badge, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+badgeColumnsSQL+badgeJoinsSQL+`
		WHERE badges.enabled AND badges.grant_mode = 'automatic'
		ORDER BY badges.id`)
	if err != nil {
		return nil, fmt.Errorf("list automatic badges: %w", err)
	}
	defer rows.Close()
	items := make([]Badge, 0, 16)
	for rows.Next() {
		badge, err := scanBadge(rows)
		if err != nil {
			return nil, fmt.Errorf("scan automatic badge: %w", err)
		}
		items = append(items, badge)
	}
	return items, rows.Err()
}

// AffectedUserIDs 返回持有该徽章的用户。
func (s *PostgresStore) AffectedUserIDs(ctx context.Context, badgeID int64) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT user_id FROM user_badges WHERE badge_id = $1`, badgeID)
	if err != nil {
		return nil, fmt.Errorf("affected users: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan affected user: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ListGrants 返回授予记录（admin 面），可按用户/徽章过滤。
func (s *PostgresStore) ListGrants(ctx context.Context, input GrantListInput) (GrantList, error) {
	conditions := make([]string, 0, 2)
	args := make([]any, 0, 4)
	if input.UserID > 0 {
		args = append(args, input.UserID)
		conditions = append(conditions, fmt.Sprintf("user_badges.user_id = $%d", len(args)))
	}
	if input.BadgeID > 0 {
		args = append(args, input.BadgeID)
		conditions = append(conditions, fmt.Sprintf("user_badges.badge_id = $%d", len(args)))
	}
	where := ""
	if len(conditions) > 0 {
		where = " WHERE " + strings.Join(conditions, " AND ")
	}
	limit := input.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	offset := input.Offset
	if offset < 0 {
		offset = 0
	}

	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM user_badges`+where, args...).Scan(&total); err != nil {
		return GrantList{}, fmt.Errorf("count grants: %w", err)
	}

	args = append(args, limit, offset)
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT user_badges.user_id, users.username, users.display_name,
		       user_badges.badge_id, badges.slug, user_badges.granted_at,
		       user_badges.granted_by, user_badges.grant_reason
		FROM user_badges
		JOIN users ON users.id = user_badges.user_id
		JOIN badges ON badges.id = user_badges.badge_id%s
		ORDER BY user_badges.granted_at DESC, user_badges.id DESC
		LIMIT $%d OFFSET $%d`, where, len(args)-1, len(args)), args...)
	if err != nil {
		return GrantList{}, fmt.Errorf("list grants: %w", err)
	}
	defer rows.Close()
	items := make([]BadgeGrantRecord, 0, limit)
	for rows.Next() {
		var record BadgeGrantRecord
		if err := rows.Scan(&record.UserID, &record.Username, &record.DisplayName,
			&record.BadgeID, &record.BadgeSlug, &record.GrantedAt,
			&record.GrantedBy, &record.GrantReason); err != nil {
			return GrantList{}, fmt.Errorf("scan grant: %w", err)
		}
		items = append(items, record)
	}
	return GrantList{Items: items, Total: total}, rows.Err()
}

// GrantAutomatic 幂等授予：已持有则不插入，返回 false。
func (s *PostgresStore) GrantAutomatic(ctx context.Context, userID int64, badgeID int64) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		VALUES ($1, $2, NULL, '')
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING
		RETURNING id`, userID, badgeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("grant automatic badge: %w", err)
	}
	return true, nil
}

// GrantManual 手动授予：已持有返回 ErrBadgeAlreadyGranted。
func (s *PostgresStore) GrantManual(ctx context.Context, input GrantInput) (bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING
		RETURNING id`, input.UserID, input.BadgeID, nullInt64(input.GrantedBy), input.GrantReason).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrBadgeAlreadyGranted
	}
	if err != nil {
		return false, fmt.Errorf("grant manual badge: %w", err)
	}
	return true, nil
}

// RevokeManual 收回授予行；不存在返回 false。
func (s *PostgresStore) RevokeManual(ctx context.Context, userID int64, badgeID int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM user_badges WHERE user_id = $1 AND badge_id = $2`, userID, badgeID)
	if err != nil {
		return false, fmt.Errorf("revoke badge: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// RecomputeTopBadge 重算单个用户的最高级徽章（可能落成 NULL）。
func (s *PostgresStore) RecomputeTopBadge(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET top_badge_id = (
			SELECT badges.id
			FROM user_badges
			JOIN badges ON badges.id = user_badges.badge_id
			JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
			WHERE user_badges.user_id = $1 AND badges.enabled
			ORDER BY `+topBadgeOrderSQL+`
			LIMIT 1
		)
		WHERE users.id = $1`, userID)
	if err != nil {
		return fmt.Errorf("recompute top badge: %w", err)
	}
	return nil
}

// GrantFromRules 按启用规则批量补授予，返回本次新插入的 user_badges 行数。
//
// 为什么是一条 SQL 而不是逐用户调 evaluator：10 万用户 × 12 规则在 Go 里循环是
// 不可接受的，在 PG 里只是一次 join。这也是「运营调低阈值后点重算就能追溯授予」
// 这条承诺的唯一实现——只修 top_badge_id 的话，按钮在阈值场景下什么也不做。
//
// granted_by = NULL + grant_reason = 'recompute' 让授予记录能区分三种来源：
// 规则自动（NULL/”）、管理员手动（actor/理由）、批量重算（NULL/'recompute'）。
func (s *PostgresStore) GrantFromRules(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO user_badges (user_id, badge_id, granted_by, grant_reason)
		SELECT activity.user_id, rules.id, NULL, 'recompute'
		FROM user_activity AS activity
		JOIN badges AS rules
		  ON rules.enabled
		 AND rules.grant_mode = 'automatic'
		 AND rules.metric IS NOT NULL
		 AND rules.threshold IS NOT NULL
		 AND rules.threshold > 0
		WHERE CASE rules.metric
		        WHEN 'topic_count'         THEN activity.topic_count
		        WHEN 'comment_count'       THEN activity.comment_count
		        WHEN 'like_received_count' THEN activity.like_received_count
		        WHEN 'like_given_count'    THEN activity.like_given_count
		        WHEN 'bookmark_count'      THEN activity.bookmark_count
		        WHEN 'days_visited'        THEN activity.days_visited::bigint
		        ELSE NULL
		      END >= rules.threshold
		ON CONFLICT ON CONSTRAINT user_badges_unique DO NOTHING`)
	if err != nil {
		return 0, fmt.Errorf("grant badges from rules: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RecomputeAll 全量重算 top_badge_id，返回实际变更的行数。
//
// 覆盖两类人：有授予行的（可能该升/该降）与 top_badge_id 非空的（徽章被删/停用
// 后可能该清空）。只写真正变化的行，避免无谓的 users 表 touch。
func (s *PostgresStore) RecomputeAll(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE users
		SET top_badge_id = best.badge_id
		FROM (
			SELECT users.id AS user_id,
			       (SELECT badges.id
			        FROM user_badges
			        JOIN badges ON badges.id = user_badges.badge_id
			        JOIN badge_tiers AS t ON t.key = badges.tier AND t.enabled
			        WHERE user_badges.user_id = users.id AND badges.enabled
			        ORDER BY `+topBadgeOrderSQL+`
			        LIMIT 1) AS badge_id
			FROM users
			WHERE users.top_badge_id IS NOT NULL
			   OR EXISTS (SELECT 1 FROM user_badges WHERE user_badges.user_id = users.id)
		) AS best
		WHERE users.id = best.user_id
		  AND users.top_badge_id IS DISTINCT FROM best.badge_id`)
	if err != nil {
		return 0, fmt.Errorf("recompute all top badges: %w", err)
	}
	return tag.RowsAffected(), nil
}

func nullInt64(value int64) any {
	if value <= 0 {
		return nil
	}
	return value
}

var _ Store = (*PostgresStore)(nil)

// pgconn 引用保证导入不被裁剪（nullInt64 之外的写路径都用 pgx）。
var _ pgconn.CommandTag
