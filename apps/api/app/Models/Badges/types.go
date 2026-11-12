package badges

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// BadgeTier 是徽章档位的 key（等级表 badge_tiers.key 的引用）。
//
// 它刻意仍是 string 别名而不是枚举：等级自 202611120001 起是**数据**（运营可增删），
// Go 侧再维护一份枚举就必然漂移——那种漂移的表现是「保存成功但排序/展示错档」。
// 合法性由等级表（外键）与 TierReader 校验，不再由本类型自证。
type BadgeTier string

// 三个内置档位的 key。它们由迁移种子行保证存在，代码里只在需要引用内置语义时使用
// （例如删除等级的 migrateTo 兜底、测试夹具），不再参与任何排序判断——排序一律读
// badge_tiers.position。
const (
	TierBronze BadgeTier = "bronze"
	TierSilver BadgeTier = "silver"
	TierGold   BadgeTier = "gold"
)

// TierAppearance 是徽章的等级外观：双语名、识别色、展示位次。
//
// 它是**actor 无关**的纯展示数据，因此可以安全地随主题/评论进缓存（BadgeRef 也带它）。
// 名字是双语并列而不是「已按 locale 解析的一个串」：同一个响应可能被不同语言的
// 读者共享（缓存、SSR 复用），在服务端按 locale 选一个就等于把 locale 写进缓存键。
//
// 空值（NameZh/NameEn/Color 全空）表示「该等级的引用已失效」——外键 RESTRICT 让
// 这在生产里不会发生，但读路径不该因此 panic 或输出半个对象。
type TierAppearance struct {
	NameZh   string
	NameEn   string
	Color    string
	Position int
}

// Empty 报告外观是否为空（未解析到等级行）。
func (a TierAppearance) Empty() bool {
	return a.NameZh == "" && a.NameEn == "" && a.Color == ""
}

// GrantMode 是授予方式。
type GrantMode string

const (
	GrantAutomatic GrantMode = "automatic"
	GrantManual    GrantMode = "manual"
)

// Valid 报告授予方式是否在枚举内（与迁移里的 grant_mode CHECK 一致）。
func (m GrantMode) Valid() bool {
	return m == GrantAutomatic || m == GrantManual
}

// Badge 是 admin 面的徽章定义，含运营状态与聚合统计。
//
// 公开面**不得**复用这个结构：enabled / grantedCount / 审计时间戳都不该下发。
// 两条面各自有结构（见 PublicBadge），靠 omitempty 遮字段迟早漏。
type Badge struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	GroupID     *int64    `json:"groupId"`
	GroupName   *string   `json:"groupName"`
	GroupKey    *string   `json:"groupKey"`
	Tier        BadgeTier `json:"tier"`
	// Appearance 是 tier 指向的等级行外观（双语名/颜色/位次）。
	Appearance TierAppearance `json:"tierAppearance"`
	Icon       string         `json:"icon"`
	GrantMode  GrantMode      `json:"grantMode"`
	// Metric / Threshold 是 202611120002 之前的旧列：**写路径不再维护、求值不再读取**，
	// 只作为 down 迁移的还原依据与后台只读展示保留一版。
	Metric    *ActivityMetric `json:"metric"`
	Threshold *int64          `json:"threshold"`
	// Rule 是自动徽章的判定规则 AST（`badges.rule`）。自动徽章唯一认它；
	// 手工徽章为 nil。解码失败时为 nil 且 RuleRaw 非空（见 rule_health 的失效清单）。
	Rule    *ruleengine.Node `json:"-"`
	RuleRaw json.RawMessage  `json:"-"`
	// RequirementSummary* 是运营填的双语要求文案；空串表示由规则自动生成。
	RequirementSummaryZh string    `json:"-"`
	RequirementSummaryEn string    `json:"-"`
	Enabled              bool      `json:"enabled"`
	Position             int       `json:"position"`
	GrantedCount         int64     `json:"grantedCount"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

// PublicBadge 是公开面的徽章定义。
//
// 刻意不含 groupId / groupName（组名由 BadgeGroupSection 统一下发，避免每张卡重复）、
// enabled（视图已过滤）、grantedCount（运营统计）、createdAt / updatedAt（审计）。
type PublicBadge struct {
	ID          int64           `json:"id"`
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	GroupKey    *string         `json:"groupKey"`
	Tier        BadgeTier       `json:"tier"`
	Appearance  TierAppearance  `json:"tierAppearance"`
	Icon        string          `json:"icon"`
	GrantMode   GrantMode       `json:"grantMode"`
	Metric      *ActivityMetric `json:"metric"`
	Threshold   *int64          `json:"threshold"`
	Position    int             `json:"position"`
	// RequirementText 是公开面的双语「获得条件」自然语言。
	// 运营填的优先；为空且规则是单条件时由服务端生成；多条件规则留空
	// （宁可没有说明，也不把内部结构拼成一句读不懂的话）。
	// **绝不包含 AST**：公开响应里出现 rule/cmp/params/children/op 会让规则结构
	// 变成事实上的公开契约。
	RequirementText RequirementText `json:"requirementText"`
}

// Public 从 admin 结构派生公开结构（丢掉运营与审计字段）。
func (b Badge) Public() PublicBadge {
	return PublicBadge{
		ID:          b.ID,
		Slug:        b.Slug,
		Name:        b.Name,
		Description: b.Description,
		GroupKey:    b.GroupKey,
		Tier:        b.Tier,
		Appearance:  b.Appearance,
		Icon:        b.Icon,
		GrantMode:   b.GrantMode,
		Metric:      b.Metric,
		Threshold:   b.Threshold,
		Position:    b.Position,

		RequirementText: BuildRequirementText(b),
	}
}

// BadgeGroup 是 admin 面的徽章分组，含组内徽章数。
type BadgeGroup struct {
	ID          int64     `json:"id"`
	Key         string    `json:"key"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Position    int       `json:"position"`
	BadgeCount  int64     `json:"badgeCount"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// BadgeProgress 是「未获得」徽章的进度。手动徽章没有阈值，Threshold 为 0。
type BadgeProgress struct {
	Current   int64 `json:"current"`
	Threshold int64 `json:"threshold"`
}

// Done 报告进度是否已达阈值；手动徽章（Threshold 为 0）永远为 false。
func (p BadgeProgress) Done() bool {
	return p.Threshold > 0 && p.Current >= p.Threshold
}

// UserBadge 是资料页徽章墙上的一格：徽章定义 + 是否已获得 + 进度。
type UserBadge struct {
	Badge     PublicBadge   `json:"badge"`
	Earned    bool          `json:"earned"`
	GrantedAt *time.Time    `json:"grantedAt"`
	Progress  BadgeProgress `json:"progress"`
}

// BadgeGroupSection 是资料页的分组外壳：组名只在这里出现一次。
type BadgeGroupSection struct {
	Key         string      `json:"key"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Position    int         `json:"position"`
	Badges      []UserBadge `json:"badges"`
}

// BadgeRef 是帖子/评论旁展示的「最高级徽章」引用。
//
// actor 无关（同一个作者的徽章对所有人一样），因此可以随主题/评论进缓存。
// 只有四个字段：这是 12px chip 能渲染的全部信息。
type BadgeRef struct {
	Slug string    `json:"slug"`
	Name string    `json:"name"`
	Tier BadgeTier `json:"tier"`
	// Appearance 随徽章一起下发：chip 的配色与双语名必须与后台配置一致，
	// 而 tier 只是 key——前端拿 key 自己映射颜色就会在运营自定义色时失效。
	Appearance TierAppearance `json:"tierAppearance"`
	Icon       string         `json:"icon"`
}

// BadgeGrantRecord 是 admin 面的授予记录，含授予人与理由。
// 这些字段**永远**不出现在公开面。
type BadgeGrantRecord struct {
	UserID      int64     `json:"userId"`
	Username    string    `json:"username"`
	DisplayName string    `json:"displayName"`
	BadgeID     int64     `json:"badgeId"`
	BadgeSlug   string    `json:"badgeSlug"`
	GrantedAt   time.Time `json:"grantedAt"`
	GrantedBy   *int64    `json:"grantedBy"`
	GrantReason string    `json:"grantReason"`
}

// normalizeSlug 归一化徽章 slug：小写、仅字母数字与连字符。
func normalizeSlug(raw string) string {
	trimmed := strings.TrimSpace(strings.ToLower(raw))
	var builder strings.Builder
	for _, r := range trimmed {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			builder.WriteRune(r)
		case r == '_' || r == ' ':
			builder.WriteRune('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}
