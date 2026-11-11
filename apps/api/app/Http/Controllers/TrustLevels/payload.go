package trustlevelscontroller

import (
	trustlevels "github.com/keeptrue/sforum/apps/api/app/Models/TrustLevels"
)

// publicPayload 把策略投影成公开目录（五档定义 + 阈值 + 限额 + 账龄闸）。
func buildPublicPayload(policy trustlevels.Policy) publicPayload {
	return buildPayload(policy, false)
}

// buildAdminPayload 是后台读面：带 limits（运营需要它来配）。
func buildAdminPayload(policy trustlevels.Policy) publicPayload {
	return buildPayload(policy, true)
}

func buildPayload(policy trustlevels.Policy, includeLimits bool) publicPayload {
	levels := make([]levelDefinition, 0, trustlevels.LevelCount)
	for level := trustlevels.LevelNewcomer; level < trustlevels.LevelCount; level++ {
		requirement := policy.Levels[level]
		limits := policy.Limits[level]
		levels = append(levels, levelDefinition{
			Level:     int(level),
			LevelSlug: level.Slug(),
			Requirements: trustlevelsRequirement{
				DaysVisited:       requirement.DaysVisited,
				TopicCount:        requirement.TopicCount,
				CommentCount:      requirement.CommentCount,
				LikeReceivedCount: requirement.LikeReceivedCount,
				LikeGivenCount:    requirement.LikeGivenCount,
				BookmarkCount:     requirement.BookmarkCount,
			},
			Limits: func() *trustLevelLimits {
				if !includeLimits {
					return nil
				}
				return &trustLevelLimits{
					TopicCooldownSeconds:   limits.TopicCooldownSeconds,
					CommentCooldownSeconds: limits.CommentCooldownSeconds,
					DailyTopicLimit:        limits.DailyTopicLimit,
					DailyCommentLimit:      limits.DailyCommentLimit,
				}
			}(),
			Earnable: nonNilStrings(policy.Earnable.Permissions(level)),
		})
	}
	return publicPayload{Levels: levels, NewUserDays: policy.NewUserDays}
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
