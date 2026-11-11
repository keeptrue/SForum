package badges

import (
	"strings"
	"time"
)

// BadgeTier 是徽章档位。权重只用于 top_badge_id 的排序，不参与展示。
type BadgeTier string

const (
	TierBronze BadgeTier = "bronze"
	TierSilver BadgeTier = "silver"
	TierGold   BadgeTier = "gold"
)

// Valid 报告档位是否在枚举内（与迁移里的 tier CHECK 一致）。
func (t BadgeTier) Valid() bool {
	switch t {
	case TierBronze, TierSilver, TierGold:
		return true
	default:
		return false
	}
}

// Weight 返回档位权重：gold > silver > bronze。
// top_badge_id 的排序第一关键字就是它，必须与资料页展示顺序一致。
func (t BadgeTier) Weight() int {
	switch t {
	case TierGold:
		return 3
	case TierSilver:
		return 2
	case TierBronze:
		return 1
	default:
		return 0
	}
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
	ID           int64           `json:"id"`
	Slug         string          `json:"slug"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	GroupID      *int64          `json:"groupId"`
	GroupName    *string         `json:"groupName"`
	GroupKey     *string         `json:"groupKey"`
	Tier         BadgeTier       `json:"tier"`
	Icon         string          `json:"icon"`
	GrantMode    GrantMode       `json:"grantMode"`
	Metric       *ActivityMetric `json:"metric"`
	Threshold    *int64          `json:"threshold"`
	Enabled      bool            `json:"enabled"`
	Position     int             `json:"position"`
	GrantedCount int64           `json:"grantedCount"`
	CreatedAt    time.Time       `json:"createdAt"`
	UpdatedAt    time.Time       `json:"updatedAt"`
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
	Icon        string          `json:"icon"`
	GrantMode   GrantMode       `json:"grantMode"`
	Metric      *ActivityMetric `json:"metric"`
	Threshold   *int64          `json:"threshold"`
	Position    int             `json:"position"`
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
		Icon:        b.Icon,
		GrantMode:   b.GrantMode,
		Metric:      b.Metric,
		Threshold:   b.Threshold,
		Position:    b.Position,
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
	Icon string    `json:"icon"`
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
