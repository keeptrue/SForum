package forum

import "database/sql"

// 徽章列在读路径上的投影与 JOIN。
//
// 徽章是 actor 无关的（同一作者的徽章对所有人一样），所以它可以随主题/评论
// 一起进缓存——这与 likedByMe / bookmarkedByMe 必须每请求重算完全不同。
// 三个 SQL 构造器共用这两个片段，保证列顺序与扫描顺序永远一致。

// topBadgeSelectSQL 是四列徽章投影，必须追加在列尾。
// 追加在列尾而不是插在中间：扫描器是位置扫描，插中间会让所有后续列错位。
func topBadgeSelectSQL() string {
	return `top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon`
}

// topBadgeJoinSQL 关联作者的最高级徽章。users 别名在各 SQL 里都指向作者。
// 徽章停用后不再展示：top_badge_id 可能仍指向停用徽章，这里按 enabled 过滤掉。
func topBadgeJoinSQL() string {
	return `
		LEFT JOIN badges top_badge ON top_badge.id = users.top_badge_id AND top_badge.enabled`
}

// badgeRefFromNulls 把 LEFT JOIN 出来的四列转成 BadgeRef。
//
// 四个值都可能为 NULL（作者没有徽章、徽章被停用、users 行缺失）。
// 直接扫进 string 会在「没有徽章」时报错——而本地种子数据人人有徽章时，
// 这条路径根本跑不到，所以必须用 NullString 接再判 Valid。
func badgeRefFromNulls(slug, name, tier, icon sql.NullString) *BadgeRef {
	if !slug.Valid || slug.String == "" {
		return nil
	}
	return &BadgeRef{
		Slug: slug.String,
		Name: name.String,
		Tier: tier.String,
		Icon: icon.String,
	}
}
