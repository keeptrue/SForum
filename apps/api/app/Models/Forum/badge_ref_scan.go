package forum

import "database/sql"

// 徽章列在读路径上的投影与 JOIN。
//
// 徽章是 actor 无关的（同一作者的徽章对所有人一样），所以它可以随主题/评论
// 一起进缓存——这与 likedByMe / bookmarkedByMe 必须每请求重算完全不同。
// 三个 SQL 构造器共用这两个片段，保证列顺序与扫描顺序永远一致。

// topBadgeSelectSQL 是七列徽章投影（4 列徽章 + 3 列等级外观），必须追加在列尾。
// 追加在列尾而不是插在中间：扫描器是位置扫描，插中间会让所有后续列错位。
//
// 等级外观（双语名 + 识别色）由 SQL 一次带出，不再让前端按 tier key 猜颜色：
// 自 202611120001 起等级是运营可改的数据（可新增档位、换颜色），前端硬编码映射
// 在新增档位时会退化成「无颜色」。
//
// COALESCE 只为「没有可展示徽章」那一行服务（LEFT JOIN 给全 NULL）：扫描侧用
// NullString 接，但 pgx 不接受把 NULL 扫进 string。徽章一旦被投影出来，等级行必然
// 存在且启用（见 topBadgeJoinSQL 的 EXISTS），所以外观不会是空串。
func topBadgeSelectSQL() string {
	return `top_badge.slug, top_badge.name, top_badge.tier, top_badge.icon,
		COALESCE(top_badge_tier.name_zh,''), COALESCE(top_badge_tier.name_en,''), COALESCE(top_badge_tier.color,'')`
}

// topBadgeJoinSQL 关联作者**当前展示**的徽章及其等级外观。users 别名在各 SQL 里都指向作者。
//
// 两个可见性条件都不可省：
//   - `top_badge.enabled`：徽章停用后不再展示（top_badge_id 可能仍指向它）；
//   - 等级启用：自 202611120001 起「停用一档等价于这一档不存在」。停档 + 重算未跑
//     的窗口里，chip 与佩戴面板必须同时判「这枚不可展示」，否则面板说「已回落到
//     silver」而 chip 还挂着 gold。
//
// 为什么用 EXISTS 而不是 JOIN 等级表：INNER JOIN 会把「没有徽章」的行整行丢掉
// （列表少行），LEFT JOIN + IS NOT NULL 判别则要改扫描器。EXISTS 只把 top_badge
// 这一组列置 NULL，行本身保留。
//
// 外观 JOIN（top_badge_tier）只负责取名称/颜色，不参与可见性判定。
func topBadgeJoinSQL() string {
	return `
		LEFT JOIN badges top_badge ON top_badge.id = users.top_badge_id AND top_badge.enabled
			AND EXISTS (SELECT 1 FROM badge_tiers AS top_badge_tier_ok WHERE top_badge_tier_ok.key = top_badge.tier AND top_badge_tier_ok.enabled)
		LEFT JOIN badge_tiers AS top_badge_tier ON top_badge_tier.key = top_badge.tier`
}

// badgeRefFromNulls 把 LEFT JOIN 出来的七列转成 BadgeRef。
//
// 七个值都可能为 NULL（作者没有徽章、徽章被停用、等级停用/缺失、users 行缺失）。
// 直接扫进 string 会在「没有徽章」时报错——而本地种子数据人人有徽章时，
// 这条路径根本跑不到，所以必须用 NullString 接再判 Valid。
func badgeRefFromNulls(slug, name, tier, icon, tierNameZh, tierNameEn, tierColor sql.NullString) *BadgeRef {
	if !slug.Valid || slug.String == "" {
		return nil
	}
	return &BadgeRef{
		Slug: slug.String,
		Name: name.String,
		Tier: tier.String,
		Icon: icon.String,
		TierName: BadgeTierName{
			Zh: tierNameZh.String,
			En: tierNameEn.String,
		},
		TierColor: tierColor.String,
	}
}
