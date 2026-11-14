package trustlevels

import "encoding/json"

// builtinTierIdentity 是内置档位的**身份**（id + 双语名）。
type builtinTierIdentity struct {
	ID   string
	Name LocalizedName
}

// builtinTierIdentities 是内置档位身份表，按 rank 顺序（TL0 起）。
//
// 这是内置档位 id 字面量**唯一**的出处（D-N5）：其余代码只通过
// BuiltinTierID / BuiltinTierName / IsBuiltinTierID 查这里。内置 id 是对外契约
// （levelSlug），存量站点的配置就是这些值，改名/插入档位都不许动它们。
//
// TL0 也在表里：它不在 Policy.Tiers 里，但契约与 UI 仍需要一个稳定 id 和名字。
var builtinTierIdentities = []builtinTierIdentity{
	{ID: NewcomerID, Name: LocalizedName{Zh: "新成员", En: "Newcomer"}},
	{ID: "member", Name: LocalizedName{Zh: "正式用户", En: "Member"}},
	{ID: "resident", Name: LocalizedName{Zh: "常驻用户", En: "Resident"}},
	{ID: "veteran", Name: LocalizedName{Zh: "资深用户", En: "Veteran"}},
	{ID: "leader", Name: LocalizedName{Zh: "社区领袖", En: "Leader"}},
}

// BuiltinTierID 返回内置档位的 id；越界返回空串。
func BuiltinTierID(rank Level) string {
	if rank >= LevelNewcomer && int(rank) < len(builtinTierIdentities) {
		return builtinTierIdentities[rank].ID
	}
	return ""
}

// BuiltinTierName 返回内置档位的双语名；越界返回零值。
func BuiltinTierName(rank Level) LocalizedName {
	if rank >= LevelNewcomer && int(rank) < len(builtinTierIdentities) {
		return builtinTierIdentities[rank].Name
	}
	return LocalizedName{}
}

// IsBuiltinTierID 报告 id 是否属于内置档位（含 newcomer）。
func IsBuiltinTierID(id string) bool {
	for _, identity := range builtinTierIdentities {
		if identity.ID == id {
			return true
		}
	}
	return false
}

// DefaultPolicy 是内置推荐阈值（运营可在后台覆盖，并支持一键恢复）。
//
// 形状是 v2 的档位数组，**数值与「定长五档」时代逐项一致**：升级不该顺手改运营预期。
//
// 取值原则：**保守**。默认 earnable 是**空集**（D17）：`SeedMemberPermissions` 已经
// 给了默认成员全部非管理内容键，所以「TL2 解锁 topic.create」对默认安装是零行为
// 变化的假功能。等级的真实默认价值是分档限额 + 等级展示 + 进度提示。
//
// NewUserDays 的默认值与 options 的 trust.new_user_days 一致（7 天）；decode 时
// 会被 option 里的值覆盖，这里只是兜底。
func DefaultPolicy() Policy {
	// 按 rank 顺序排列的推荐阈值：数组位置决定 rank（第 0 项 = TL1），所以这里
	// 不需要「第几档」的具名常量——序号的来源只有一个，就是顺序本身。
	defaults := []Requirements{
		// TL1 保守：不要求发主题、不要求点赞（点赞依赖别人的行为，小站会卡死），
		// 只要求「跨 3 天回来 + 5 条回复」。见 capability-map.md §5.2。
		{DaysVisited: 3, CommentCount: 5},
		{
			DaysVisited:       15,
			TopicCount:        2,
			CommentCount:      20,
			LikeReceivedCount: 5,
			LikeGivenCount:    3,
		},
		{
			DaysVisited:       45,
			TopicCount:        8,
			CommentCount:      80,
			LikeReceivedCount: 30,
			LikeGivenCount:    20,
			BookmarkCount:     5,
		},
		{
			DaysVisited:       100,
			TopicCount:        25,
			CommentCount:      250,
			LikeReceivedCount: 100,
			LikeGivenCount:    80,
			BookmarkCount:     20,
		},
	}
	tiers := make([]Tier, 0, len(defaults))
	for index, requirement := range defaults {
		tiers = append(tiers, newDefaultTier(Level(index+1), requirement))
	}
	return Policy{NewUserDays: 7, Tiers: tiers}
}

// RestoreDefaults 把策略恢复成推荐默认，同时**保留删除墓碑**。
//
// 「恢复默认」是整份覆盖（D-N15），但它不能悄悄打破同一份契约里「id 永不复用」的
// 承诺：被删掉的 tier_<n> 若不进新配置的墓碑，下一次新建档就会把同一个 id 派给
// 另一档，而旧 id 可能已经出现在审计、契约快照与运营文档里。
//
// 墓碑取「既有墓碑 + 本次被移除的 tier_<n> 档位 id」的保序并集。内置档位 id 不进
// 墓碑：NextTierSeq 的基准是 len(builtinTierIdentities)（=5），内置四档即使被移除
// 也不会被复用，留墓碑只是噪声。
func RestoreDefaults(previous Policy) Policy {
	next := DefaultPolicy()
	deleted := make([]string, 0, len(previous.DeletedTierIDs)+len(previous.Tiers))
	seen := make(map[string]bool, len(deleted))
	appendID := func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		deleted = append(deleted, id)
	}
	for _, id := range previous.DeletedTierIDs {
		appendID(id)
	}
	for _, tier := range previous.Tiers {
		if _, ok := tierSeq(tier.ID); ok {
			appendID(tier.ID)
		}
	}
	if len(deleted) > 0 {
		next.DeletedTierIDs = deleted
	}
	return next
}

// newDefaultTier 造一个内置档位：id/name/icon 全部来自内置表，限额全继承。
//
// TL1 到 TL4 默认**全部继承站点默认**（D27）：放宽必须由运营在后台显式配，
// 代码里不发明「高等级绕过站级冷却」这种看不见也关不掉的行为。
func newDefaultTier(rank Level, requirement Requirements) Tier {
	return Tier{
		ID:           BuiltinTierID(rank),
		Name:         BuiltinTierName(rank),
		Icon:         DefaultTierIcon(rank),
		Requirements: requirement,
		Limits:       LevelLimits{},
		Earnable:     nil,
		Retired:      false,
	}
}

// RuleOf 返回某 rank 的可选规则树；无规则 / 越界返回 nil。
//
// rank 与档位下标的折算规则只有一处：**只数 active 档位**（停用档不占 rank，D-N2）。
// 这与 `RequirementsOf` / `TierAt` 同一套，任何一格错位都会让规则挂到别的档上——
// 而那种错位在阈值相近时几乎看不出来。
func (p Policy) RuleOf(rank Level) json.RawMessage {
	if !rank.Valid() || rank <= LevelNewcomer {
		return nil
	}
	index := Level(0)
	for _, tier := range p.Tiers {
		if tier.Retired {
			continue
		}
		index++
		if index == rank {
			return tier.Rule
		}
	}
	return nil
}

// HasRules 报告策略里是否有**启用档位**带了规则。
//
// 它是「要不要为这次判定构造求值器」的唯一判据：无规则站点必须零新增查询（验收 §6.1），
// 所以构造求值器（= 建一个 conditions.Resolver）之前先看这一眼。停用档不算——它不参与
// 判定，规则也不会被求值。
func (p Policy) HasRules() bool {
	for _, tier := range p.Tiers {
		if tier.Retired {
			continue
		}
		if len(tier.Rule) > 0 {
			return true
		}
	}
	return false
}
