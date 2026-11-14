package badgescontroller

import (
	"encoding/json"
	"time"

	badges "github.com/keeptrue/sforum/apps/api/app/Models/Badges"
	"github.com/keeptrue/sforum/apps/api/app/Models/Conditions"
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
	// Description 是徽章详情弹窗的正文（`?badge=<slug>` 深链与点击卡片共用）。
	// 没有 omitempty：空串是有效值（运营没写说明），缺键会被前端当成「响应没实现」。
	Description string `json:"description"`
	// Tier 仍是等级 key（前端 data-tier 与 URL 用它），TierName/TierColor 是展示用外观。
	Tier      string      `json:"tier"`
	TierName  tierNameDTO `json:"tierName"`
	TierColor string      `json:"tierColor"`
	Icon      string      `json:"icon"`
	Position  int         `json:"position"`
	Earned    bool        `json:"earned"`
	// RequirementText 与目录同源：用户看到徽章想「怎么拿」是同一个需求。
	RequirementText requirementTextDTO `json:"requirementText"`
	Current         *int64             `json:"current,omitempty"`
	Threshold       *int64             `json:"threshold,omitempty"`
	GrantedAt       *time.Time         `json:"grantedAt,omitempty"`
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
		Slug:        entry.Badge.Slug,
		Name:        entry.Badge.Name,
		Description: entry.Badge.Description,
		Tier:        string(entry.Badge.Tier),
		TierName:    toTierName(entry.Badge.Appearance),
		TierColor:   entry.Badge.Appearance.Color,
		Icon:        entry.Badge.Icon,
		Position:    entry.Badge.Position,
		Earned:      entry.Earned,

		RequirementText: toRequirementText(entry.Badge.RequirementText),
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

// badgeDisplayRef 是展示位里的一枚徽章引用（`GET/PUT/DELETE /badges/worn` 共用）。
//
// 与 `profileBadgeItem` / `publicBadgeCatalogItem` 是同一套字段命名：`tier` 是等级
// key，外观拆成 `tierName{zh,en}` + `tierColor`。**不是**领域 `BadgeRef` 的原样序列化：
// 那边的外观字段是 Go 字段名（`tierAppearance.NameZh` / `NameEn` / `Color` / `Position`，
// `TierAppearance` 没有 json tag），同一份数据两种形状会让前端写两套解析。
//
// 所有字段都没有 omitempty：外观为空（等级引用失效）时 `tierName` 仍是 `{"zh":"","en":""}`，
// 缺键会让前端把「等级没配名」误判成「响应没实现」。
type badgeDisplayRef struct {
	Slug      string      `json:"slug"`
	Name      string      `json:"name"`
	Tier      string      `json:"tier"`
	TierName  tierNameDTO `json:"tierName"`
	TierColor string      `json:"tierColor"`
	Icon      string      `json:"icon"`
}

// badgeDisplayResponse 是三条佩戴端点共用的响应体（`badges.BadgeDisplay` 的投影）。
//
// 三个引用字段都**没有** omitempty：值为 null 时键必须照常出现。前端按 `mode` 分支
// 渲染展示位，「键缺失」与「这个位子是空的」是两种状态，前者会被当成加载失败。
type badgeDisplayResponse struct {
	Mode      string           `json:"mode"`
	Worn      *badgeDisplayRef `json:"worn"`
	Automatic *badgeDisplayRef `json:"automatic"`
	Upgrade   *badgeDisplayRef `json:"upgrade"`
}

// toBadgeDisplay 把领域展示态投影成 wire 形状。nil 指针保持 nil（JSON 出 null）。
func toBadgeDisplay(display badges.BadgeDisplay) badgeDisplayResponse {
	return badgeDisplayResponse{
		Mode:      display.Mode,
		Worn:      toBadgeDisplayRef(display.Worn),
		Automatic: toBadgeDisplayRef(display.Automatic),
		Upgrade:   toBadgeDisplayRef(display.Upgrade),
	}
}

func toBadgeDisplayRef(ref *badges.BadgeRef) *badgeDisplayRef {
	if ref == nil {
		return nil
	}
	return &badgeDisplayRef{
		Slug:      ref.Slug,
		Name:      ref.Name,
		Tier:      string(ref.Tier),
		TierName:  toTierName(ref.Appearance),
		TierColor: ref.Appearance.Color,
		Icon:      ref.Icon,
	}
}

// publicBadgeCatalogResponse 是公开徽章目录（GET /api/v1/badges）的 wire 形状。
//
// 键集合是白名单，不是「admin DTO 减去几个」：
//   - 不出现 metric：指标是运营调参，公开即不可撤回（计划 §3.3 第 2 条）。
//     注意本结构上**没有**对应字段——用 omitempty 让字段存在但不出现在 JSON 里
//     不算合规，那是漏写 tag 就泄露的形状。
//   - 不出现 enabled / grantedCount / createdAt / updatedAt / id：运营与审计字段。
//
// 进度（current / threshold）是**查看者装饰**，不是定义字段：只对「未获得 + 可测」
// 的条目出现，由 HTTP 层每请求合并（见 toPublicCatalog）。定义级阈值仍然不下发——
// `PublicCatalogEntry` 上根本没有 Threshold 字段。
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
	// RequirementText 是公开面的双语「获得条件」。领域层由 `Badge.Public()` 填好，
	// 这里必须原样带出——「过滤后重建结构」最容易漏的就是这类新字段。
	RequirementText requirementTextDTO `json:"requirementText"`
	// Current / Threshold 是查看者的进度：只对「未获得 + 自动 + 可测」的规则徽章出现。
	// 已获得 / 匿名 / 不可测（lte、布尔、多条件组）都不输出这两个键。
	// current=0 是有效值，所以用指针 + omitempty，而不是 int64（那会吃掉 0）。
	Current   *int64 `json:"current,omitempty"`
	Threshold *int64 `json:"threshold,omitempty"`
}

// requirementTextDTO 是双语要求文案。两语言都下发，前端按 locale 选。
type requirementTextDTO struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

func toRequirementText(text badges.RequirementText) requirementTextDTO {
	return requirementTextDTO{Zh: text.Zh, En: text.En}
}

// toPublicCatalog 把领域目录映射成 wire 形状，并按查看者合并 earned 与进度。
//
// earned / progress 都由调用方每请求单独取（`EarnedBadgeSlugs` /
// `ViewerCatalogProgress`），本函数只做查表；领域目录结构里没有这两个字段，
// 所以「actor 相关数据进缓存」在这里无从发生。
//
// 进度只挂在**未获得**的条目上：已获得的行已经有徽章本身，再给一条满格进度条
// 只是噪音；匿名时 progress 为 nil，两个键都不出现。
func toPublicCatalog(catalog badges.PublicCatalog, earned map[string]struct{}, progress map[string]badges.BadgeProgress) publicBadgeCatalogResponse {
	groups := make([]publicBadgeCatalogGroup, 0, len(catalog.Groups))
	for _, group := range catalog.Groups {
		items := make([]publicBadgeCatalogItem, 0, len(group.Badges))
		for _, entry := range group.Badges {
			_, granted := earned[entry.Slug]
			item := publicBadgeCatalogItem{
				Slug:        entry.Slug,
				Name:        entry.Name,
				Description: entry.Description,
				Icon:        entry.Icon,
				Tier:        string(entry.Tier),
				TierName:    toTierName(entry.Appearance),
				TierColor:   entry.Appearance.Color,
				Group:       entry.GroupKey,
				Earned:      granted,

				RequirementText: toRequirementText(entry.RequirementText),
			}
			if !granted {
				if measure, ok := progress[entry.Slug]; ok && measure.Threshold > 0 {
					current, threshold := measure.Current, measure.Threshold
					item.Current = &current
					item.Threshold = &threshold
				}
			}
			items = append(items, item)
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
//
// Rule 是 `badges.rule` 的**原始字节** `{"v":1,"root":{...}}`，不是重新 marshal
// 的 `*ruleengine.Node`：
//   - 重新 marshal 会丢掉解码器不认识的字段（规则写入方是后台表单，但导入、
//     M3 插件或回滚版本的规则可能带本版本不认识的键），运营打开编辑器再保存
//     就等于静默改写规则；
//   - 解码失败的脏规则在领域层是 `Rule=nil` + `RuleRaw` 非空（见 rule_health），
//     按 Node 序列化会把它变成 `null`，编辑器就再也看不到「这条规则坏在哪」。
//
// 手写徽章 / 无规则时**输出 `null`**（键必须在）：前端据此区分「没有规则」与
// 「字段没下发」，后者会被当成读取失败并禁用编辑器。
//
// RequirementSummary 无 omitempty：空串是有效值（表示「按规则自动生成文案」），
// 缺键会让前端把「运营清空了文案」误判为「后端没实现」。
type adminBadgeResponse struct {
	ID          int64           `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	GroupID     *int64          `json:"groupId"`
	GroupName   *string         `json:"groupName"`
	Tier        string          `json:"tier"`
	TierName    tierNameDTO     `json:"tierName"`
	TierColor   string          `json:"tierColor"`
	Icon        string          `json:"icon"`
	GrantMode   string          `json:"grantMode"`
	Metric      *string         `json:"metric"`
	Threshold   *int64          `json:"threshold"`
	Rule        json.RawMessage `json:"rule"`
	// RequirementSummary 已废弃（deprecated）：2026-11 起公开说明只由规则生成，
	// 本字段仅为回滚与旧客户端兼容保留，**后台不再读写它**。
	RequirementSummary requirementTextDTO `json:"requirementSummary"`
	// RequirementText 是服务端按规则生成的文案（与公开面同一实现）。
	// 保存后立即生效；后台把两个手写框改成它的只读预览。
	RequirementText requirementTextDTO `json:"requirementText"`
	Enabled         bool               `json:"enabled"`
	Position        int                `json:"position"`
	GrantedCount    int64              `json:"grantedCount"`
	CreatedAt       time.Time          `json:"createdAt"`
	UpdatedAt       time.Time          `json:"updatedAt"`
}

func toAdminBadge(badge badges.Badge) adminBadgeResponse {
	var metric *string
	if badge.Metric != nil {
		value := string(*badge.Metric)
		metric = &value
	}
	// 只认 RuleRaw：领域结构里 `Rule` 是解码结果，脏规则时为 nil，
	// 用它当 wire 来源会把「有规则但解不开」渲染成「没有规则」。
	// 空字节归一成 nil，让 encoding/json 输出 `null` 而不是空串（后者不是合法 JSON）。
	var rule json.RawMessage
	if len(badge.RuleRaw) > 0 {
		rule = append(json.RawMessage(nil), badge.RuleRaw...)
	}
	// 文案由域层填好（`AdminService` 一次批量解析真名）；单条读（GetBadge）没有
	// 那一步，用泛称兜底现算——「admin 面也有文案」是契约，不能靠调用顺序成立。
	text := badge.RequirementText
	if text.Zh == "" || text.En == "" {
		text = badges.BuildRequirementText(badge, conditions.NewRefNames())
	}
	return adminBadgeResponse{
		ID: badge.ID, Slug: badge.Slug, Name: badge.Name, Description: badge.Description,
		GroupID: badge.GroupID, GroupName: badge.GroupName,
		Tier: string(badge.Tier), TierName: toTierName(badge.Appearance),
		TierColor: badge.Appearance.Color, Icon: badge.Icon, GrantMode: string(badge.GrantMode),
		Metric: metric, Threshold: badge.Threshold,
		Rule:               rule,
		RequirementSummary: requirementTextDTO{Zh: badge.RequirementSummaryZh, En: badge.RequirementSummaryEn},
		RequirementText:    toRequirementText(text),
		Enabled:            badge.Enabled,
		Position:           badge.Position, GrantedCount: badge.GrantedCount,
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

// conditionParamDTO 是一个条件参数。字段名与 M2 编辑器契约逐字一致。
type conditionParamDTO struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Min      *int64   `json:"min,omitempty"`
	Max      *int64   `json:"max,omitempty"`
	Options  []string `json:"options,omitempty"`
	RefKind  string   `json:"refKind,omitempty"`
}

// conditionDTO 是 `GET /admin/badges/conditions` 的一条。
type conditionDTO struct {
	Key        string              `json:"key"`
	ValueKind  string              `json:"valueKind"`
	Compare    []string            `json:"compare"`
	LabelZh    string              `json:"labelZh"`
	LabelEn    string              `json:"labelEn"`
	DescribeZh string              `json:"describeZh,omitempty"`
	DescribeEn string              `json:"describeEn,omitempty"`
	UnitZh     string              `json:"unitZh,omitempty"`
	UnitEn     string              `json:"unitEn,omitempty"`
	Params     []conditionParamDTO `json:"params"`
}

// factCandidateDTO 是插件事实候选（`factCandidates[]`）的 wire 形状。
//
// 与 `conditionDTO` 分开：候选有自己的双语 label/unit、比较运算符子集与 variant 列表，
// 塞进条件 DTO 会让「条件名」与「下拉项文案」两个概念继续纠缠。
//
// **除 `unit` 外都没有 omitempty**：键在值空（`"compare": []` 被省略成缺键）会让
// 编辑器按 undefined 渲染，而空数组/空字符串在这里是合法且有意义的取值。
// `unit` 用 null 表示「这个条件没有计量单位」，所以是唯一的指针字段。
type factCandidateDTO struct {
	Key      string              `json:"key"`
	Label    requirementTextDTO  `json:"label"`
	Unit     *requirementTextDTO `json:"unit"`
	Compare  []string            `json:"compare"`
	Variants []factVariantDTO    `json:"variants"`
}

// factVariantDTO 是一个 variant 候选。
type factVariantDTO struct {
	Value string             `json:"value"`
	Label requirementTextDTO `json:"label"`
}

func toFactCandidateDTO(candidate conditions.FactCandidate) factCandidateDTO {
	dto := factCandidateDTO{
		Key:      candidate.Key,
		Label:    requirementTextDTO{Zh: candidate.LabelZh, En: candidate.LabelEn},
		Compare:  make([]string, 0, len(candidate.Compare)),
		Variants: make([]factVariantDTO, 0, len(candidate.Variants)),
	}
	if candidate.UnitZh != "" || candidate.UnitEn != "" {
		dto.Unit = &requirementTextDTO{Zh: candidate.UnitZh, En: candidate.UnitEn}
	}
	dto.Compare = append(dto.Compare, candidate.Compare...)
	for _, variant := range candidate.Variants {
		dto.Variants = append(dto.Variants, factVariantDTO{
			Value: variant.Value,
			Label: requirementTextDTO{Zh: variant.LabelZh, En: variant.LabelEn},
		})
	}
	return dto
}

// refCandidateDTO 是 objectRef 参数的一个候选（下拉项）。
//
// 与 `factCandidateDTO` 分开：这里只有「写进 params 的值 + 给人看的名字 + 为什么
// 不该选」，没有比较运算符、variant、计量单位。`flags` 是数组而不是 bool：未来
// 可能同时命中多种标记（例如一个既隐藏又停用的对象）。
type refCandidateDTO struct {
	Value string   `json:"value"`
	Label string   `json:"label"`
	Flags []string `json:"flags"`
}

// refCandidateGroupDTO 是一类候选。五类永远是五项、固定顺序，由领域层保证。
//
// 三个字段都**没有** omitempty：`flags: []` 被省略成缺键会让前端按 undefined 渲染
// （与 factCandidates 同一条裁决）；`unavailable:false` 也必须显式出现，否则前端
// 无法区分「能取到且为空」与「根本没下发」。
type refCandidateGroupDTO struct {
	Kind        string            `json:"kind"`
	Candidates  []refCandidateDTO `json:"candidates"`
	Unavailable bool              `json:"unavailable"`
	Truncated   bool              `json:"truncated"`
}

func toRefCandidateDTO(candidate conditions.RefCandidate) refCandidateDTO {
	flags := make([]string, 0, len(candidate.Flags))
	flags = append(flags, candidate.Flags...)
	return refCandidateDTO{
		Value: candidate.Value,
		Label: candidate.Label,
		Flags: flags,
	}
}

// toRefCandidateSetDTO 把领域集合投影成 wire 数组。
//
// 恒为数组而不是 null：编辑器对 null 与 `[]` 的处理不同（前者会被当成「没下发」
// 触发整体兜底），而 `NormalizeRefCandidateSet` 已经保证五项齐全、顺序固定。
func toRefCandidateSetDTO(set conditions.RefCandidateSet) []refCandidateGroupDTO {
	groups := make([]refCandidateGroupDTO, 0, len(set))
	for _, group := range set {
		candidates := make([]refCandidateDTO, 0, len(group.Candidates))
		for _, candidate := range group.Candidates {
			candidates = append(candidates, toRefCandidateDTO(candidate))
		}
		groups = append(groups, refCandidateGroupDTO{
			Kind:        group.Kind,
			Candidates:  candidates,
			Unavailable: group.Unavailable,
			Truncated:   group.Truncated,
		})
	}
	return groups
}

func conditionToDTO(definition conditions.Definition) conditionDTO {
	dto := conditionDTO{
		Key:        definition.Key,
		ValueKind:  string(definition.ValueKind),
		Compare:    make([]string, 0, len(definition.Compare)),
		LabelZh:    definition.LabelZh,
		LabelEn:    definition.LabelEn,
		DescribeZh: definition.DescribeZh,
		DescribeEn: definition.DescribeEn,
		UnitZh:     definition.UnitZh,
		UnitEn:     definition.UnitEn,
		Params:     make([]conditionParamDTO, 0, len(definition.Params.Specs)),
	}
	for _, op := range definition.Compare {
		dto.Compare = append(dto.Compare, string(op))
	}
	for _, spec := range definition.Params.Specs {
		param := conditionParamDTO{
			Name:     spec.Name,
			Kind:     string(spec.Kind),
			Required: spec.Required,
			Options:  spec.Options,
			RefKind:  spec.RefKind,
		}
		if spec.Min != 0 {
			value := spec.Min
			param.Min = &value
		}
		if spec.Max != 0 {
			value := spec.Max
			param.Max = &value
		}
		dto.Params = append(dto.Params, param)
	}
	return dto
}

// recomputeResponse 是 `POST /admin/badges/recompute` 的 wire 形状。
//
// 三个计数器缺一不可，且**键集合必须与 OpenAPI `BadgeRecomputeResult.required` 一致**：
//   - granted   = 本次按规则补出的授予行数；
//   - refreshed = `top_badge_id` 实际变化的人数；
//   - queued    = 转入异步评估的**徽章数**（不是用户数）。
//
// 为什么抽成结构体而不是 handler 里的 map 字面量：内联字面量只能靠源码 grep 守，
// 抓不到「字段名拼错」「新增字段漏加」这类问题；抽出来之后可以真单测（见
// `TestRecomputeResponseCarriesAllCounters`）。本字段曾因内联映射漏写而丢失，
// 导致契约与实现不一致（M1.4-21）。
type recomputeResponse struct {
	Granted   int64 `json:"granted"`
	Refreshed int64 `json:"refreshed"`
	Queued    int64 `json:"queued"`
}

// toRecomputeResponse 把领域结果投影成 wire 形状。
func toRecomputeResponse(result badges.RecomputeResult) recomputeResponse {
	return recomputeResponse{
		Granted:   result.Granted,
		Refreshed: result.Refreshed,
		Queued:    result.Queued,
	}
}
