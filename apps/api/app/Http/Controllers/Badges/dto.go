package badgescontroller

import (
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
)

// 本文件只做「领域结构 → 扁平 JSON」的映射，不写任何业务逻辑：
// 不排序、不算进度、不判可见性。那些都在领域层，改这里不该改变行为。

// profileBadgeItem 是资料页徽章墙上的一格（扁平形状，前端已按此实现）。
//
// 指针字段表达「没有这个键」而不是「值是 0」：
//   - current / threshold 只在未获得的自动徽章上出现（进度条要用）；
//     已获得条目不输出这两个键。current=0 是有效值，用 omitempty 会把它吃掉。
//   - grantedAt 只在 earned=true 时出现，未获得条目不返回 null。
type profileBadgeItem struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Tier 仍是等级 key（前端 data-tier 与 URL 用它），TierName/TierColor 是展示用外观。
	Tier      string      `json:"tier"`
	TierName  tierNameDTO `json:"tierName"`
	TierColor string      `json:"tierColor"`
	Icon      string      `json:"icon"`
	Position  int         `json:"position"`
	Earned    bool        `json:"earned"`
	Current   *int64      `json:"current,omitempty"`
	Threshold *int64      `json:"threshold,omitempty"`
	GrantedAt *time.Time  `json:"grantedAt,omitempty"`
}

// tierNameDTO 是等级的双语名。两个语言都下发，前端按当前 locale 选：
// 服务端选一个就等于把 locale 写进响应，而这份响应是可被共享缓存的（资料页徽章墙）。
type tierNameDTO struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// toTierName 把领域外观投影成 wire 形状。
func toTierName(appearance badges.TierAppearance) tierNameDTO {
	return tierNameDTO{Zh: appearance.NameZh, En: appearance.NameEn}
}

type profileBadgeGroup struct {
	Key      string             `json:"key"`
	Name     string             `json:"name"`
	Position int                `json:"position"`
	Badges   []profileBadgeItem `json:"badges"`
}

type profileBadgesResponse struct {
	Groups []profileBadgeGroup `json:"groups"`
}

// toProfileBadges 把领域分区映射成前端契约形状。
func toProfileBadges(sections []badges.BadgeGroupSection) profileBadgesResponse {
	groups := make([]profileBadgeGroup, 0, len(sections))
	for _, section := range sections {
		items := make([]profileBadgeItem, 0, len(section.Badges))
		for _, entry := range section.Badges {
			items = append(items, toProfileBadgeItem(entry))
		}
		groups = append(groups, profileBadgeGroup{
			Key:      section.Key,
			Name:     section.Name,
			Position: section.Position,
			Badges:   items,
		})
	}
	return profileBadgesResponse{Groups: groups}
}

func toProfileBadgeItem(entry badges.UserBadge) profileBadgeItem {
	item := profileBadgeItem{
		Slug:      entry.Badge.Slug,
		Name:      entry.Badge.Name,
		Tier:      string(entry.Badge.Tier),
		TierName:  toTierName(entry.Badge.Appearance),
		TierColor: entry.Badge.Appearance.Color,
		Icon:      entry.Badge.Icon,
		Position:  entry.Badge.Position,
		Earned:    entry.Earned,
	}
	if entry.Earned {
		// 已获得：只给 grantedAt，不给进度条数据。
		item.GrantedAt = entry.GrantedAt
		return item
	}
	// 未获得：只有自动徽章有阈值，手动徽章两者都不输出。
	if entry.Badge.GrantMode == badges.GrantAutomatic && entry.Progress.Threshold > 0 {
		current := entry.Progress.Current
		threshold := entry.Progress.Threshold
		item.Current = &current
		item.Threshold = &threshold
	}
	return item
}

// publicBadgeCatalogResponse 是公开徽章目录（GET /api/v1/badges）的 wire 形状。
//
// 键集合是白名单，不是「admin DTO 减去几个」：
//   - 不出现 threshold / metric：阈值是运营调参，公开即不可撤回（计划 §3.3 第 2 条）。
//     注意本结构上**没有**对应字段——用 omitempty 让字段存在但不出现在 JSON 里
//     不算合规，那是漏写 tag 就泄露的形状。
//   - 不出现 enabled / grantedCount / createdAt / updatedAt / id：运营与审计字段。
//
// truncated 在顶层而不是分组级：截断是整份响应的属性，不是某个分组的属性。
type publicBadgeCatalogResponse struct {
	Groups    []publicBadgeCatalogGroup `json:"groups"`
	Truncated bool                      `json:"truncated"`
}

type publicBadgeCatalogGroup struct {
	Key string `json:"key"`
	// Name 为空表示未分组（或该分组没配名字），展示文案由前端 i18n 决定：
	// 领域层不硬编码「未分组」，否则 en-US 站点会收到中文。
	Name   string                   `json:"name"`
	Badges []publicBadgeCatalogItem `json:"badges"`
}

// publicBadgeCatalogItem 是目录里的一枚徽章。Group 是该徽章所属分组的 key
// （未分组为空串），让条目自描述：前端做筛选或平铺时不必回看父分组。
type publicBadgeCatalogItem struct {
	Slug        string      `json:"slug"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Icon        string      `json:"icon"`
	Tier        string      `json:"tier"`
	TierName    tierNameDTO `json:"tierName"`
	TierColor   string      `json:"tierColor"`
	Group       string      `json:"group"`
	Earned      bool        `json:"earned"`
}

// toPublicCatalog 把领域目录映射成 wire 形状，并按查看者合并 earned。
//
// earned 由调用方每请求单独取（badges.Service.EarnedBadgeSlugs），本函数只做查表；
// 领域目录结构里没有 earned 字段，所以「actor 相关数据进缓存」在这里无从发生。
func toPublicCatalog(catalog badges.PublicCatalog, earned map[string]struct{}) publicBadgeCatalogResponse {
	groups := make([]publicBadgeCatalogGroup, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		items := make([]publicBadgeCatalogItem, 0, len(group.Badges))
		for _, entry := range group.Badges {
			_, granted := earned[entry.Slug]
			items = append(items, publicBadgeCatalogItem{
				Slug:        entry.Slug,
				Name:        entry.Name,
				Description: entry.Description,
				Icon:        entry.Icon,
				Tier:        string(entry.Tier),
				TierName:    toTierName(entry.Appearance),
				TierColor:   entry.Appearance.Color,
				Group:       entry.GroupKey,
				Earned:      granted,
			})
		}
		groups = append(groups, publicBadgeCatalogGroup{
			Key:    group.Key,
			Name:   group.Name,
			Badges: items,
		})
	}
	return publicBadgeCatalogResponse{Groups: groups, Truncated: catalog.Truncated}
}

// adminBadgeResponse 是后台列表的一行：公开面不得出现这些字段。
type adminBadgeResponse struct {
	ID           int64       `json:"id"`
	Slug         string      `json:"slug"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	GroupID      *int64      `json:"groupId"`
	GroupName    *string     `json:"groupName"`
	Tier         string      `json:"tier"`
	TierName     tierNameDTO `json:"tierName"`
	TierColor    string      `json:"tierColor"`
	Icon         string      `json:"icon"`
	GrantMode    string      `json:"grantMode"`
	Metric       *string     `json:"metric"`
	Threshold    *int64      `json:"threshold"`
	Enabled      bool        `json:"enabled"`
	Position     int         `json:"position"`
	GrantedCount int64       `json:"grantedCount"`
	CreatedAt    time.Time   `json:"createdAt"`
	UpdatedAt    time.Time   `json:"updatedAt"`
}

func toAdminBadge(badge badges.Badge) adminBadgeResponse {
	var metric *string
	if badge.Metric != nil {
		value := string(*badge.Metric)
		metric = &value
	}
	return adminBadgeResponse{
		ID: badge.ID, Slug: badge.Slug, Name: badge.Name, Description: badge.Description,
		GroupID: badge.GroupID, GroupName: badge.GroupName,
		Tier: string(badge.Tier), TierName: toTierName(badge.Appearance),
		TierColor: badge.Appearance.Color, Icon: badge.Icon, GrantMode: string(badge.GrantMode),
		Metric: metric, Threshold: badge.Threshold, Enabled: badge.Enabled,
		Position: badge.Position, GrantedCount: badge.GrantedCount,
		CreatedAt: badge.CreatedAt, UpdatedAt: badge.UpdatedAt,
	}
}

func toAdminBadges(items []badges.Badge) []adminBadgeResponse {
	out := make([]adminBadgeResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toAdminBadge(item))
	}
	return out
}

type adminBadgeGroupResponse struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	BadgeCount  int64     `json:"badgeCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func toAdminBadgeGroup(group badges.BadgeGroup) adminBadgeGroupResponse {
	return adminBadgeGroupResponse{
		ID: group.ID, Key: group.Key, Name: group.Name, Description: group.Description,
		Position: group.Position, BadgeCount: group.BadgeCount,
		CreatedAt: group.CreatedAt, UpdatedAt: group.UpdatedAt,
	}
}

func toAdminBadgeGroups(items []badges.BadgeGroup) []adminBadgeGroupResponse {
	out := make([]adminBadgeGroupResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toAdminBadgeGroup(item))
	}
	return out
}

// adminBadgeTierResponse 是后台等级表的一行。
//
// name 是 {zh,en} 对象而不是两个扁平字段：前端已有「按 locale 取名」的 helper，
// 扁平字段会让每个调用点再拼一次 `locale === 'en' ? nameEn : nameZh`。
//
// badgeCount 不是运营字段而是**决策依据**：删除前必须知道有多少徽章要迁移。
type adminBadgeTierResponse struct {
	Key        string      `json:"key"`
	Name       tierNameDTO `json:"name"`
	Icon       string      `json:"icon"`
	Color      string      `json:"color"`
	Position   int         `json:"position"`
	Enabled    bool        `json:"enabled"`
	BadgeCount int64       `json:"badgeCount"`
}

func toAdminBadgeTier(tier badges.TierDef) adminBadgeTierResponse {
	return adminBadgeTierResponse{
		Key:        tier.Key,
		Name:       tierNameDTO{Zh: tier.NameZh, En: tier.NameEn},
		Icon:       tier.Icon,
		Color:      tier.Color,
		Position:   tier.Position,
		Enabled:    tier.Enabled,
		BadgeCount: tier.BadgeCount,
	}
}

// adminBadgeTiersResponse 是 GET /admin/badges/tiers 的整份响应。
//
// colorChoices 与 items 同体下发是刻意的：色板白名单属于服务端（Support/Palette），
// 前端自己抄一份就会在服务端调整时变成「UI 里能选、保存时 422」。
type adminBadgeTiersResponse struct {
	Items        []adminBadgeTierResponse `json:"items"`
	ColorChoices []string                 `json:"colorChoices"`
}

func toAdminBadgeTiers(items []badges.TierDef, colorChoices []string) adminBadgeTiersResponse {
	out := make([]adminBadgeTierResponse, 0, len(items))
	for _, item := range items {
		out = append(out, toAdminBadgeTier(item))
	}
	return adminBadgeTiersResponse{Items: out, ColorChoices: colorChoices}
}

// publicBadgeTierItem 是公开等级目录的一行。
//
// 刻意不含 enabled / badgeCount / 审计字段：公开面只回答「站内有哪几档、叫什么、
// 什么颜色、什么顺序」。停用档位由 SQL 过滤，不出现在这里。
type publicBadgeTierItem struct {
	Key      string      `json:"key"`
	Name     tierNameDTO `json:"name"`
	Icon     string      `json:"icon"`
	Color    string      `json:"color"`
	Position int         `json:"position"`
}

func toPublicBadgeTiers(items []badges.TierDef) []publicBadgeTierItem {
	out := make([]publicBadgeTierItem, 0, len(items))
	for _, item := range items {
		out = append(out, publicBadgeTierItem{
			Key:      item.Key,
			Name:     tierNameDTO{Zh: item.NameZh, En: item.NameEn},
			Icon:     item.Icon,
			Color:    item.Color,
			Position: item.Position,
		})
	}
	return out
}

// grantCandidateItem 是批量授予选择器的一行。
//
// status 与 alreadyGranted 都是**提示**而不是过滤条件：服务端不替运营决定
// 「能不能给一个已封禁用户补发历史活动徽章」，只把事实摆出来。
type grantCandidateItem struct {
	UserID         int64  `json:"userId"`
	Username       string `json:"username"`
	DisplayName    string `json:"displayName"`
	Status         string `json:"status"`
	AlreadyGranted bool   `json:"alreadyGranted"`
}

func toGrantCandidates(items []badges.GrantCandidate) []grantCandidateItem {
	out := make([]grantCandidateItem, 0, len(items))
	for _, item := range items {
		out = append(out, grantCandidateItem{
			UserID:         item.UserID,
			Username:       item.Username,
			DisplayName:    item.DisplayName,
			Status:         item.Status,
			AlreadyGranted: item.AlreadyGranted,
		})
	}
	return out
}

// batchGrantFailureItem 是单个人的失败原因；reason 是稳定错误码，不是日志串。
type batchGrantFailureItem struct {
	UserID   int64  `json:"userId"`
	Username string `json:"username"`
	Reason   string `json:"reason"`
}

// batchGrantResultResponse 是批量授予的结果。
//
// 三个计数分开返回，且**部分成功不是错误状态码**：运营需要看到
// 「48 成功 / 2 已持有 / 1 失败」这种事实，而不是一个 500 之后不知道做成了多少。
type batchGrantResultResponse struct {
	Granted int                     `json:"granted"`
	Skipped int                     `json:"skipped"`
	Failed  []batchGrantFailureItem `json:"failed"`
}

func toBatchGrantResult(result badges.BatchGrantResult) batchGrantResultResponse {
	failed := make([]batchGrantFailureItem, 0, len(result.Failed))
	for _, item := range result.Failed {
		failed = append(failed, batchGrantFailureItem{
			UserID:   item.UserID,
			Username: item.Username,
			Reason:   item.Reason,
		})
	}
	return batchGrantResultResponse{Granted: result.Granted, Skipped: result.Skipped, Failed: failed}
}
