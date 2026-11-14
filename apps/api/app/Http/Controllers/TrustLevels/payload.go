package trustlevelscontroller

import (
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// publicPayload 把策略投影成公开目录（active 档位定义 + 阈值 + 账龄闸）。
//
// TL0 是**合成**的第一条：它不在 trust.levels 里（语义由 trust.new_user_* 决定），
// 但 UI 需要一个统一的档位列表，所以这里补一条 editable:false 的只读项。
func buildPublicPayload(policy trustlevels.Policy) publicPayload {
	return publicPayload{Levels: buildPublicLevels(policy), NewUserDays: policy.NewUserDays}
}

// buildPublicLevels 投影公开档位列表。
//
// 它**只**产出 `publicLevelDefinition`——这个类型里没有 Limits、没有 Rule，所以
// 「公开面漏出后台字段」不是靠纪律避免的，而是编译期不可能。
func buildPublicLevels(policy trustlevels.Policy) []publicLevelDefinition {
	levels := make([]publicLevelDefinition, 0, len(policy.ActiveTiers())+1)
	levels = append(levels, newcomerDefinition())
	for rank := trustlevels.LevelMember; rank <= policy.LastRank(); rank++ {
		tier, ok := policy.TierAt(rank)
		if !ok {
			continue
		}
		levels = append(levels, toPublicLevel(rank, tier, policy))
	}
	return levels
}

// buildAdminLevels 投影后台档位列表：公开形状 + limits + rule。
//
// 与 `buildPublicLevels` 分开而不是传一个 bool：bool 只能表达「这次要不要 limits」，
// 表达不了「这次**不允许**有 rule」——而后者才是公开面的真实约束。
func buildAdminLevels(policy trustlevels.Policy) []adminLevelDefinition {
	levels := make([]adminLevelDefinition, 0, len(policy.ActiveTiers())+1)
	levels = append(levels, adminLevelDefinition{publicLevelDefinition: newcomerDefinition()})
	for rank := trustlevels.LevelMember; rank <= policy.LastRank(); rank++ {
		tier, ok := policy.TierAt(rank)
		if !ok {
			continue
		}
		levels = append(levels, adminLevelDefinition{
			publicLevelDefinition: toPublicLevel(rank, tier, policy),
			Limits: &trustLevelLimits{
				TopicCooldownSeconds:   tier.Limits.TopicCooldownSeconds,
				CommentCooldownSeconds: tier.Limits.CommentCooldownSeconds,
				DailyTopicLimit:        tier.Limits.DailyTopicLimit,
				DailyCommentLimit:      tier.Limits.DailyCommentLimit,
			},
			Rule: tier.Rule,
		})
	}
	return levels
}

// toPublicLevel 投影一档的两个面共有的部分（公开面字段全集）。
func toPublicLevel(rank trustlevels.Level, tier trustlevels.Tier, policy trustlevels.Policy) publicLevelDefinition {
	return publicLevelDefinition{
		Level:       int(rank),
		LevelSlug:   tier.ID,
		DisplayName: toTrustLevelName(tier.Name),
		Icon:        tier.Icon,
		// 有效色：显式覆盖优先，否则按 rank 取默认色序（空串不下发）。
		Color: policy.ColorOf(rank),
		Requirements: trustlevelsRequirement{
			DaysVisited:       tier.Requirements.DaysVisited,
			TopicCount:        tier.Requirements.TopicCount,
			CommentCount:      tier.Requirements.CommentCount,
			LikeReceivedCount: tier.Requirements.LikeReceivedCount,
			LikeGivenCount:    tier.Requirements.LikeGivenCount,
			BookmarkCount:     tier.Requirements.BookmarkCount,
		},
		Earnable: nonNilStrings(policy.Earnable.Permissions(rank)),
		Retired:  false,
		Editable: true,
	}
}

// newcomerDefinition 合成 TL0 条目。
//
// editable:false 是硬语义，不是 UI 提示：TL0 的阈值/限额由 trust.new_user_* 七键与
// 基线门决定，`trust.levels` 里给它留位置只会制造第二份真相。
// retired:false 同理——它不是「停用的档位」，而是「不在这份配置里的档位」。
func newcomerDefinition() publicLevelDefinition {
	return publicLevelDefinition{
		Level:       int(trustlevels.LevelNewcomer),
		LevelSlug:   trustlevels.NewcomerID,
		DisplayName: toTrustLevelName(trustlevels.BuiltinTierName(trustlevels.LevelNewcomer)),
		Icon:        trustlevels.DefaultTierIcon(trustlevels.LevelNewcomer),
		Color:       trustlevels.DefaultTierColor(trustlevels.LevelNewcomer),
		Earnable:    []string{},
		Retired:     false,
		Editable:    false,
	}
}

func toTrustLevelName(name trustlevels.LocalizedName) trustLevelName {
	return trustLevelName{Zh: name.Zh, En: name.En}
}

// toTrustLevelNamePool 把命名池投影成 wire 形状，并保证两个数组都不是 null。
func toTrustLevelNamePool(pool trustlevels.NamePool) trustLevelNamePool {
	return trustLevelNamePool{
		Zh: nonNilStrings(pool.Zh),
		En: nonNilStrings(pool.En),
	}
}

// nonNilStrings 保证 JSON 里是 `[]` 而不是 `null`。
//
// `earnable` 在契约里是 required 的数组：null 满足「字段存在」但不满足「它是数组」，
// 任何 `for (const k of earnable)` / `earnable.length` 都会炸。而默认 earnable 是
// 空集（D17），也就是说默认安装下**每一档**都会踩到这条路径。
func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
