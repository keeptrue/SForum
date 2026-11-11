package badges

import "context"

// PublicCatalogLimit 是公开目录的硬上限。
//
// 徽章是运营配置量级（默认安装十几个），分页是为不存在的规模付复杂度；
// 但无上限的公开列表不可接受。超限截断并置 Truncated，让前端能提示而不是静默少几条。
const PublicCatalogLimit = 500

// PublicCatalogEntry 是公开目录里的一枚徽章。
//
// 刻意不复用 PublicBadge：那个结构带 Metric / Threshold，而阈值是运营调参，
// 公开即不可撤回（计划 §3.3 第 2 条）。让类型上不存在这两个字段，比在 HTTP DTO 上
// 靠 omitempty 挡住更可靠——少写一个 tag 就会泄露，字段不存在则不可能泄露。
type PublicCatalogEntry struct {
	Slug        string
	Name        string
	Description string
	Icon        string
	Tier        BadgeTier
	// GroupKey 为空表示未分组。
	GroupKey string
	// GroupName 为空有两种情况：未分组，或该分组没有名字。
	// 未分组的展示文案由前端 i18n 提供，领域层不硬编码中文。
	GroupName string
}

// PublicCatalogGroup 是公开目录的一个分组外壳。空分组不会出现。
type PublicCatalogGroup struct {
	Key    string
	Name   string
	Badges []PublicCatalogEntry
}

// PublicCatalog 是公开目录的读结果（actor 无关）。
//
// 这里**没有** earned 字段，是刻意的：earned 是查看者相关的，由 EarnedBadgeSlugs
// 单独取、只在 HTTP 层合并。让结构上不存在该字段，是「earned 绝不进共享缓存」的
// 机械保证——没有字段就没有被缓存携带的可能（对照 Forum 的 LikedByMe /
// BookmarkedByMe：每请求装饰、永不进缓存，见 app/Models/Forum/types.go:328-333）。
type PublicCatalog struct {
	Groups    []PublicCatalogGroup
	Truncated bool
}

// PublicCatalog 返回启用徽章的公开目录。
//
// 排序由 ListPublicCatalog 的 SQL 给出，本函数只做稳定分区、不重排。资料页的
// buildSections 之所以要在 Go 层再排一次分组，是因为它的分组顺序与组内顺序来自
// 两处（SQL 给组内、Go 给组间）；这里只有一个来源，多一套规则就会漂，
// 而那正是「换存储或加过滤条件时被漏掉」的典型。
func (s *Service) PublicCatalog(ctx context.Context) (PublicCatalog, error) {
	if s == nil || s.store == nil {
		return PublicCatalog{Groups: []PublicCatalogGroup{}}, nil
	}
	entries, truncated, err := s.store.ListPublicCatalog(ctx, PublicCatalogLimit)
	if err != nil {
		return PublicCatalog{}, err
	}
	return PublicCatalog{Groups: groupCatalogEntries(entries), Truncated: truncated}, nil
}

// EarnedBadgeSlugs 返回该用户已获得的徽章 slug 集合（查看者相关）。
//
// 与 PublicCatalog 分成两个方法，是本端点缓存安全的全部依据：
//   - PublicCatalog 的签名里没有 actor/userID，按构造只能是 actor 无关的；
//   - EarnedBadgeSlugs 是唯一吃 userID 的读，结果永不进入任何共享缓存，
//     也永不写回 PublicCatalog 结构。
//
// 匿名（userID <= 0）直接返回空集合且不查库：匿名请求不该碰 user_badges。
func (s *Service) EarnedBadgeSlugs(ctx context.Context, userID int64) (map[string]struct{}, error) {
	if s == nil || s.store == nil || userID <= 0 {
		return map[string]struct{}{}, nil
	}
	return s.store.ListEarnedBadgeSlugs(ctx, userID)
}

// groupCatalogEntries 把已排好序的行切成分组，空组不产生。
//
// 用首次出现顺序建组，因此组间顺序 = SQL 的 (group.position, group.id) 顺序，
// 组内顺序 = SQL 的 (badge.position, badge.id) 顺序，两条规则同源。
func groupCatalogEntries(entries []PublicCatalogEntry) []PublicCatalogGroup {
	groups := make([]PublicCatalogGroup, 0, 4)
	index := make(map[string]int, 4)
	for _, entry := range entries {
		position, ok := index[entry.GroupKey]
		if !ok {
			position = len(groups)
			index[entry.GroupKey] = position
			groups = append(groups, PublicCatalogGroup{
				Key:    entry.GroupKey,
				Name:   entry.GroupName,
				Badges: make([]PublicCatalogEntry, 0, 4),
			})
		}
		groups[position].Badges = append(groups[position].Badges, entry)
	}
	return groups
}
