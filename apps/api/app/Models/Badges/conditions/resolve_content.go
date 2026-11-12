package conditions

import (
	"context"
	"fmt"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 内容类条件。三个都**不**过滤软删，唯一例外是 content.hidden：
// 它问的就是「有多少条被隐藏了」，不看 status 就没有意义。
const (
	// pinnedSQL：被置顶的主题数。软删主题仍计入（成就口径：他确实写过一条
	// 曾被置顶的主题；置顶事实不会因为删除而消失）。
	pinnedSQL = `
SELECT count(*) FROM topics
WHERE author_user_id = $1 AND is_pinned
`
	// hiddenSQL：被隐藏的主题数。**唯一按 status 判定的条件**，通常放在 `not`
	// 下面表达「没有违规记录」这类负向信号。
	hiddenSQL = `
SELECT count(*) FROM topics
WHERE author_user_id = $1 AND status = 'hidden'
`
	// badgeOwnedSQL：是否已拥有该徽章。不看 badges.enabled——停用只是不再展示，
	// 授予关系仍然存在，「拥有」按关系行判定。
	badgeOwnedSQL = `
SELECT candidates.slug,
       EXISTS (
         SELECT 1 FROM user_badges AS grants
           JOIN badges AS owned ON owned.id = grants.badge_id
          WHERE grants.user_id = $1 AND owned.slug = candidates.slug)
FROM unnest($2::text[]) AS candidates(slug)
`
)

func (r *Resolver) loadContent(ctx context.Context, typ string, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	var query string
	switch typ {
	case KeyContentPinned:
		query = pinnedSQL
	case KeyContentHidden:
		query = hiddenSQL
	default:
		return nil, fmt.Errorf("conditions: 未知内容条件 %q", typ)
	}
	var count int64
	if err := r.pool.QueryRow(ctx, query, r.userID).Scan(&count); err != nil {
		return nil, fmt.Errorf("conditions: 查询 %s 失败：%w", typ, err)
	}
	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = countValue(count)
	}
	return out, nil
}

func (r *Resolver) loadBadgeOwned(ctx context.Context, leaves []ruleengine.Leaf) (map[string]ruleengine.Value, error) {
	slugs := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		slugs = append(slugs, stringParam(leaf, "slug"))
	}
	rows, err := r.pool.Query(ctx, badgeOwnedSQL, r.userID, slugs)
	if err != nil {
		return nil, fmt.Errorf("conditions: 查询 badge.owned 失败：%w", err)
	}
	defer rows.Close()

	owned := make(map[string]bool, len(slugs))
	for rows.Next() {
		var slug string
		var hit bool
		if err := rows.Scan(&slug, &hit); err != nil {
			return nil, fmt.Errorf("conditions: 读取 badge.owned 结果失败：%w", err)
		}
		owned[slug] = hit
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("conditions: 遍历 badge.owned 结果失败：%w", err)
	}

	out := make(map[string]ruleengine.Value, len(leaves))
	for _, leaf := range leaves {
		slug := stringParam(leaf, "slug")
		hit, ok := owned[slug]
		if !ok {
			return nil, fmt.Errorf("conditions: badge.owned 缺少 %q 的结果", slug)
		}
		memo, err := valueKey(leaf.Type, leaf.Params)
		if err != nil {
			return nil, err
		}
		out[memo] = boolValue(hit)
	}
	return out, nil
}
