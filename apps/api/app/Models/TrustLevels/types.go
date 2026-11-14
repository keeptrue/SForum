// Package trustlevels 派生用户信任等级（TL0 + TL1..TLn）以及随之解锁的既有权限键。
//
// 设计约束（见 knowledge/decisions/2026-11-10-trust-levels.md）：
//
//   - 等级是纯函数派生值，不落列、不缓存。Level / LevelAt 不读 DB、不调
//     time.Now——now 由调用方注入，这样阈值边界测试不需要时钟替换。
//   - 已挣得权限是纯加性来源，永不否定静态授予；与「管理类权限」硬不相交。
//   - 本包**不 import** Identity / Options / Forum：阈值与权限键都在这里声明或由
//     调用方传入，依赖方向只有 Identity → TrustLevels。
//
// 档位有**身份**与**序号**两个概念（D-N1），必须分开记：
//
//   - identity 是不可变 id（newcomer / member / … / tier_<seq>）：配置、契约、
//     levelSlug 字段、已挣得映射的键都用它。改名字、改阈值、改顺序都不动 id。
//   - rank 是序号，等于「该档在 active 档位里的位次 + 1」，由配置数组顺序派生，
//     不落字段。插入一档只会让后面档位的 rank 顺移，不会改写它们的身份。
//
// TL0 固定占用 rank 0，且**不在** Policy.Tiers 里：它的语义仍由 trust.new_user_*
// 七键 + 基线门决定，本包不为它发明可配置项。
package trustlevels

import (
	"encoding/json"
	"time"
)

// Level 是信任等级，同时是 rank（序号）。零值是 TL0（新成员），与 Actor 零值语义一致。
//
// Actor.TrustLevel int 的语义保持不变：它就是 rank。因此 Identity / Forum 不需要任何
// 改动——它们看到的仍是一个 0..8 的整数。
type Level int

// 只保留两个有真实锚点的具名 rank：它们是语言里的介词，不是「第几档」。
//
//   - LevelNewcomer = 0：TL0 永远存在、永远不可配置（trust.new_user_* + 基线门）。
//   - LevelMember = 1：账龄闸封顶在这里，基线门的「TL1 起自动生效」也锚在这里。
//
// 之所以**不再**保留 resident/veteran/leader 三个常量：「TL4 是最高档」这个前提已经
// 作废，把它们留着等于让后来者继续用序号常量判顶。判顶一律用 Policy.LastRank()。
const (
	LevelNewcomer Level = 0
	LevelMember   Level = 1

	// NewcomerID 是 TL0 的身份 id。TL0 不在 Policy.Tiers 里，但契约/UI 仍需要一个
	// 稳定标识，所以它作为常量登记一次（内置档位身份表的首页）。
	NewcomerID = "newcomer"

	// MinTiers 是 active 档位数的下界：至少一档，否则「TL1 起」的判定没有落点。
	MinTiers = 1

	// MaxTiers 是**可配置**档位（TL1..TLn）的数量上限。
	MaxTiers = 8

	// MaxLevel 是合法 rank 的上界。TL0 固定占 0，所以 active 档位最多到 MaxTiers。
	MaxLevel Level = MaxTiers
)

// Valid 报告 rank 是否落在 0..MaxLevel。
//
// 它只做**语法**边界检查，不代表策略里真有这么多档：策略实际档数看 Policy.LastRank()。
func (l Level) Valid() bool { return l >= LevelNewcomer && l <= MaxLevel }

// LocalizedName 是一档的双语名称（D-N3）。
//
// 只存两种语言的具体文案，不存 i18n 键：运营可以自由命名，键无法预先枚举。
type LocalizedName struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// IsZero 报告名称是否完全为空。
func (n LocalizedName) IsZero() bool { return n.Zh == "" && n.En == "" }

// Tier 是一档可配置等级（TL1..TLn）。
//
// 它是**解码后的领域形状**；wire 形状在 config.go 的 TierConfig 里（那边需要用
// json.RawMessage 区分「省略」与「显式 null」）。两处字段必须同批修改。
type Tier struct {
	// ID 是不可变身份（D-N1 / D-N6）。只由解码期派生，不接受客户端指定。
	ID string
	// Name 是运营可改的双语显示名。
	Name LocalizedName
	// Icon 是 lucide 图标名，必须在 TierIcons() 白名单内。
	Icon string
	// Color 是档位识别色，取值必须在 TierColors() 白名单内。
	//
	// 与 Icon 不同，这里存的是**原始值**：空串表示「跟随位次自动」，由 ColorOf
	// 在读取时按 rank 折算默认色。**解码期不填默认值**——一旦把自动值冻进配置，
	// 运营之后插入档位时，原本「跟着走」的颜色就变成钉死的一次性取值。
	Color string
	// Requirements 是达到本档的最小行为量。
	Requirements Requirements
	// Limits 是本档的发言限额；nil 字段沿用站点默认。
	Limits LevelLimits
	// Earnable 是本档**恰好**解锁的权限键（不含更低档）。
	//
	// retired 档位也保留这里的配置：软停用只让判定与 UI 跳过，配置全量保留，
	// 恢复时不需要运营重新勾一遍。
	Earnable []string
	// Rule 是本档的可选规则树（M1）。nil 表示「无规则」，判定只看 Requirements。
	//
	// 存 **原始 JSON 字节**而不是 `*ruleengine.Node`：解码期校验通过的树要能原样写回，
	// 重新序列化会重排键序并让「读一次再存一次」变成一次隐式改写（config.go 的
	// TierConfig.Rule 注释里有完整理由）。
	Rule json.RawMessage
	// Retired 是软停用标记：配置保留，判定与 UI 跳过（D-N2）。
	//
	// 只软删除是 id 永不复用的前提（D-N6）：硬删除会让 tier_7 被后来者复用，
	// 而 tier_7 可能已经出现在历史审计、契约快照或运营的文档里。
	Retired bool
}

// Activity 是等级判定的行为输入，全部来自 user_activity 单行（days_visited 的真值
// 在 user_visits，但同样由统计层维护）。阅读类指标不参与判定。
type Activity struct {
	TopicCount        int64
	CommentCount      int64
	LikeReceivedCount int64
	LikeGivenCount    int64
	BookmarkCount     int64
	DaysVisited       int64
}

// Requirements 是一档等级的最小行为量，AND 语义：零值字段表示该指标不设门槛。
// JSON 字段名与 Metrics 标识严格一致：后台表单、契约、option JSON 三处共用一份
// 命名，改这里等于改三处（DisallowUnknownFields 会把漏改直接变成解码错误）。
type Requirements struct {
	DaysVisited       int64 `json:"days_visited"`
	TopicCount        int64 `json:"topic_count"`
	CommentCount      int64 `json:"comment_count"`
	LikeReceivedCount int64 `json:"like_received_count"`
	LikeGivenCount    int64 `json:"like_given_count"`
	BookmarkCount     int64 `json:"bookmark_count"`
}

// Metrics 是 Requirements 六个字段的稳定标识，也就是 trust.levels 这个**单 JSON
// 键**里 `tiers[].requirements` 的字段名（见 config.go 的 Config/TierConfig）。
// 顺序即后台表单与契约里的展示顺序。
//
// 注意：这里**不是** options 键后缀。D19 之前的「每档每指标一个扁平键
// trust.level.<n>.<metric>」方案已作废。
var Metrics = []string{
	"days_visited",
	"topic_count",
	"comment_count",
	"like_received_count",
	"like_given_count",
	"bookmark_count",
}

// Value 按 metric 标识读一个阈值；未知 metric 返回 0,false。
func (r Requirements) Value(metric string) (int64, bool) {
	switch metric {
	case "days_visited":
		return r.DaysVisited, true
	case "topic_count":
		return r.TopicCount, true
	case "comment_count":
		return r.CommentCount, true
	case "like_received_count":
		return r.LikeReceivedCount, true
	case "like_given_count":
		return r.LikeGivenCount, true
	case "bookmark_count":
		return r.BookmarkCount, true
	}
	return 0, false
}

// SetValue 按 metric 标识写一个阈值；未知 metric 返回 false。
func (r *Requirements) SetValue(metric string, value int64) bool {
	if r == nil {
		return false
	}
	switch metric {
	case "days_visited":
		r.DaysVisited = value
	case "topic_count":
		r.TopicCount = value
	case "comment_count":
		r.CommentCount = value
	case "like_received_count":
		r.LikeReceivedCount = value
	case "like_given_count":
		r.LikeGivenCount = value
	case "bookmark_count":
		r.BookmarkCount = value
	default:
		return false
	}
	return true
}

// Value 按 metric 标识读一项行为统计；未知 metric 返回 0,false。
func (a Activity) Value(metric string) (int64, bool) {
	switch metric {
	case "days_visited":
		return a.DaysVisited, true
	case "topic_count":
		return a.TopicCount, true
	case "comment_count":
		return a.CommentCount, true
	case "like_received_count":
		return a.LikeReceivedCount, true
	case "like_given_count":
		return a.LikeGivenCount, true
	case "bookmark_count":
		return a.BookmarkCount, true
	}
	return 0, false
}

// IsZero 报告该档是否完全没有门槛（用于识别「这一档永远不会被达到」的配置错误）。
func (r Requirements) IsZero() bool {
	return r == Requirements{}
}

// LevelLimits 是一档等级的发言限额。nil 表示「沿用站点默认」。
//
// 指针而不是 int：0 在这里是**有语义的**（免冷却 = 冷却 0 秒），不能与
// 「没配」混为一谈。限额只影响多频繁，不进 Can()（D8）。
type LevelLimits struct {
	TopicCooldownSeconds   *int `json:"topicCooldownSeconds"`
	CommentCooldownSeconds *int `json:"commentCooldownSeconds"`
	DailyTopicLimit        *int `json:"dailyTopicLimit"`
	DailyCommentLimit      *int `json:"dailyCommentLimit"`
}

// Policy 是等级判定的完整策略。
type Policy struct {
	// NewUserDays 是「最多当多久新成员」的上限闸：账龄不足时最高只到最低 active
	// 档（rank 1）。0 表示不设闸（任何人至少 rank 1）。
	NewUserDays int
	// Tiers 是 TL1..TLn，**数组顺序 = 判定与展示顺序**。可以包含 retired 档位：
	// 它们保留在原位置，只是不参与 rank 编号与判定。
	Tiers []Tier
	// Earnable 是「rank → 恰好解锁的既有权限键」映射，默认空集（D17）。
	// 键是 rank（TL1 起），retired 档位的配置**不进**这里（判定跳过）。
	Earnable Earnable
	// DeletedTierIDs 是**删除墓碑**：运营真正删掉的档位 id，按提交顺序。
	//
	// 墓碑不占 rank、不进判定、不进 UI，只做两件事：让 NextTierSeq 知道这些 id
	// 曾经存在（id 永不复用），以及在下次读面把「已删除」与「从没配过」区分开
	// （后者会让运营以为配置丢了）。软停用（Retired）与它是两回事：停用保留配置，
	// 删除只留墓碑。
	DeletedTierIDs []string
}

// LastRank 返回最高 active 档的 rank；没有任何 active 档时返回 TL0。
//
// 解码期保证 active 档数 ≥ 1，所以正常策略下至少是 TL1；这里的 0 只是让
// 「手搓的零值 Policy」也不会 panic。
func (p Policy) LastRank() Level {
	count := 0
	for _, tier := range p.Tiers {
		if tier.Retired {
			continue
		}
		count++
	}
	return Level(count)
}

// ActiveTiers 返回 active 档位（按判定/展示顺序），retired 已剔除。
func (p Policy) ActiveTiers() []Tier {
	out := make([]Tier, 0, len(p.Tiers))
	for _, tier := range p.Tiers {
		if tier.Retired {
			continue
		}
		out = append(out, tier)
	}
	return out
}

// activeIndex 把 rank 折算成 Tiers 下标。rank 0（TL0）没有对应档位。
func (p Policy) activeIndex(rank Level) (int, bool) {
	if rank < LevelMember || rank > MaxLevel {
		return 0, false
	}
	position := Level(0)
	for index, tier := range p.Tiers {
		if tier.Retired {
			continue
		}
		position++
		if position == rank {
			return index, true
		}
	}
	return 0, false
}

// TierAt 返回 rank 对应的档位。
func (p Policy) TierAt(rank Level) (Tier, bool) {
	index, ok := p.activeIndex(rank)
	if !ok {
		return Tier{}, false
	}
	return p.Tiers[index], true
}

// RequirementsOf 返回 rank 的最小行为量；TL0 与越界 rank 返回零值（不设门槛）。
func (p Policy) RequirementsOf(rank Level) Requirements {
	tier, ok := p.TierAt(rank)
	if !ok {
		return Requirements{}
	}
	return tier.Requirements
}

// LimitsFor 返回 rank 的限额配置；nil 字段表示沿用站点默认。
func (p Policy) LimitsFor(rank Level) LevelLimits {
	tier, ok := p.TierAt(rank)
	if !ok {
		return LevelLimits{}
	}
	return tier.Limits
}

// TierID 返回 rank 的身份 id。TL0 固定是 newcomer；越界 rank 返回空串。
func (p Policy) TierID(rank Level) string {
	if rank == LevelNewcomer {
		return NewcomerID
	}
	tier, ok := p.TierAt(rank)
	if !ok {
		return ""
	}
	return tier.ID
}

// DisplayNameOf 返回 rank 的双语名。TL0 用内置名，越界 rank 返回零值。
func (p Policy) DisplayNameOf(rank Level) LocalizedName {
	if rank == LevelNewcomer {
		return BuiltinTierName(LevelNewcomer)
	}
	tier, ok := p.TierAt(rank)
	if !ok {
		return LocalizedName{}
	}
	return tier.Name
}

// IconOf 返回 rank 的图标名；TL0 与越界 rank 走默认序列。
func (p Policy) IconOf(rank Level) string {
	if rank == LevelNewcomer {
		return DefaultTierIcon(LevelNewcomer)
	}
	tier, ok := p.TierAt(rank)
	if !ok {
		return ""
	}
	return tier.Icon
}

// ColorOf 返回 rank 的**有效色**：永远非空，可直接下发。
//
// 解析顺序：TL0 恒取默认色序首项（它不可配置）→ active 档位有显式覆盖则用它 →
// 否则按位次取默认色序；越界 rank 回退首项（同 DefaultTierColor 的兜底理由）。
//
// 与 IconOf 的差别是**这里必须解析**而不是返回空串：color 是契约里的 required 字段，
// 空串会让前端多一条「这档没颜色」的分支，而它本来只是「跟随位次」。
func (p Policy) ColorOf(rank Level) string {
	if rank == LevelNewcomer {
		return DefaultTierColor(LevelNewcomer)
	}
	tier, ok := p.TierAt(rank)
	if !ok {
		return DefaultTierColor(LevelNewcomer)
	}
	if tier.Color != "" {
		return tier.Color
	}
	return DefaultTierColor(rank)
}

// SetRequirements 改一档的阈值（后台表单与测试用）。rank 必须是 active 档。
func (p *Policy) SetRequirements(rank Level, requirement Requirements) bool {
	if p == nil {
		return false
	}
	index, ok := p.activeIndex(rank)
	if !ok {
		return false
	}
	p.Tiers[index].Requirements = requirement
	return true
}

// SetLimitsFor 改一档的限额（后台表单与测试用）。rank 必须是 active 档。
func (p *Policy) SetLimitsFor(rank Level, limits LevelLimits) bool {
	if p == nil {
		return false
	}
	index, ok := p.activeIndex(rank)
	if !ok {
		return false
	}
	p.Tiers[index].Limits = limits
	return true
}

// NextTierSeq 返回下一个新档 id 的序号（D-N6）：
// max(所有档含 retired 的数字后缀, 删除墓碑的数字后缀, 5)+1。
//
// 基准是 5 而不是 4：内置五档已经占掉 1..5 的语义位置，第一个新增档从 tier_6 起，
// 这样即使内置档位被删除/停用，tier_1..tier_5 也永远不会被新档占用。
//
// **墓碑必须计入**：删除是物理删除（配置里不再有那一档），下界只靠「现存档位」时，
// 删掉 tier_7 后新建一档就会又派生出 tier_7——而那个 id 可能已经出现在历史审计、
// 契约快照与运营文档里。计入墓碑是「id 永不复用」在物理删除下的唯一实现。
func (p Policy) NextTierSeq() int {
	maxSeq := len(builtinTierIdentities)
	for _, tier := range p.Tiers {
		if seq, ok := tierSeq(tier.ID); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	for _, id := range p.DeletedTierIDs {
		if seq, ok := tierSeq(id); ok && seq > maxSeq {
			maxSeq = seq
		}
	}
	return maxSeq + 1
}

// accountAge 把「注册时刻 → 账龄」的换算收在一处，避免各调用方各写一份。
func accountAge(createdAt, now time.Time) time.Duration {
	if createdAt.IsZero() {
		// 未知注册时间按「刚注册」处理：最严，不会因为读失败而放宽限制。
		return 0
	}
	return now.Sub(createdAt)
}
