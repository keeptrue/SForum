package badges

import (
	"context"
	"fmt"
)

// 本文件是公开目录的**读**查询。
//
// 与 postgres_store_catalog.go 分文件是遵循那边的分层声明（写路径一个文件、
// 读路径另一个）；也不塞进 postgres_store.go，避免那个 435 行的文件继续长。

// ListPublicCatalog 返回启用徽章的公开目录，最多 limit 条；truncated 表示还有更多。
//
// ORDER BY 与 ListBadges（postgres_store.go:69）、ListUserBadges（:146-148）逐字相同：
// 同一个徽章在后台列表、资料页徽章墙、公开目录里必须排在同一位置，否则运营会以为
// 配置没生效。差异只有两处，都是本查询自己的语义：
//   - WHERE badges.enabled 放进 SQL。ListBadges 在 Go 层过滤停用徽章，若照抄，
//     停用徽章会吃掉 LIMIT 名额，501 条里可能只返回 480 条启用徽章。
//   - LIMIT limit+1 探测截断。目录上限固定 500，不需要精确总数，
//     所以不照抄 ListGrants 的 COUNT(*) 第二趟。
//
// topBadgeOrderSQL（:23-31）**不参与**本查询：那是「最高级徽章」的 LIMIT 1 排序
// （tier 权重优先），拿它排目录会把 gold 徽章整体顶到最前面。
func (s *PostgresStore) ListPublicCatalog(ctx context.Context, limit int) ([]PublicCatalogEntry, bool, error) {
	if limit <= 0 || limit > PublicCatalogLimit {
		limit = PublicCatalogLimit
	}
	rows, err := s.pool.Query(ctx, `
		SELECT badges.slug, badges.name, badges.description, badges.icon, badges.tier,
		       COALESCE(badge_groups.key, ''), COALESCE(badge_groups.name, ''),
		       `+badgeTierColumnsSQL+`
		FROM badges
		LEFT JOIN badge_groups ON badge_groups.id = badges.group_id
		LEFT JOIN badge_tiers AS tier_def ON tier_def.key = badges.tier
		WHERE badges.enabled
		ORDER BY COALESCE(badge_groups.position, 2147483647), badge_groups.id NULLS LAST, badges.position, badges.id
		LIMIT $1`, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list public badge catalog: %w", err)
	}
	defer rows.Close()
	items := make([]PublicCatalogEntry, 0, limit)
	for rows.Next() {
		var entry PublicCatalogEntry
		if err := rows.Scan(&entry.Slug, &entry.Name, &entry.Description, &entry.Icon,
			&entry.Tier, &entry.GroupKey, &entry.GroupName,
			&entry.Appearance.NameZh, &entry.Appearance.NameEn,
			&entry.Appearance.Color, &entry.Appearance.Position); err != nil {
			return nil, false, fmt.Errorf("scan public badge catalog: %w", err)
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	truncated := len(items) > limit
	if truncated {
		items = items[:limit]
	}
	return items, truncated, nil
}

// ListEarnedBadgeSlugs 返回该用户已获得的**启用**徽章 slug 集合。
//
// 这是本刀唯一吃 userID 的读，只由 HTTP 层每请求装饰调用一次；
// 结果不写回任何 actor 无关结构、不进缓存（徽章 store 今天也没有缓存装饰器，
// 见 bootstrap/api_assembly_domains.go:554-556，但这个保证不能依赖装配方式）。
//
// 停用徽章不算获得：它不在目录里，标记 earned 只会让前端找不到对应条目。
func (s *PostgresStore) ListEarnedBadgeSlugs(ctx context.Context, userID int64) (map[string]struct{}, error) {
	earned := make(map[string]struct{})
	if userID <= 0 {
		return earned, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT badges.slug
		FROM user_badges
		JOIN badges ON badges.id = user_badges.badge_id
		WHERE user_badges.user_id = $1 AND badges.enabled`, userID)
	if err != nil {
		return nil, fmt.Errorf("list earned badge slugs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, fmt.Errorf("scan earned badge slug: %w", err)
		}
		earned[slug] = struct{}{}
	}
	return earned, rows.Err()
}
