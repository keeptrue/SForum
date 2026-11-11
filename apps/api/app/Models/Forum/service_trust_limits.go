package forum

import (
	"context"
	"time"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

// 信任阶梯的限额计算从 Service 方法改成自由函数（架构门禁棘轮）：
//   - 自由函数不增加 Forum.Service 的接收者计数（baseline 77，本刀一度到 79）；
//   - service_ops.go 的行数上限（1170）也同时被解除。
//
// 逻辑与判据一行未改：五道判据、TL0/TL1+ 的分档、resolveLimit 的三层优先级。
// 与 service_comment_limits.go 的分工一致：那个文件当初就是因为「service_ops.go
// 到达行数上限」才拆出来的，这里是同一情况的第三次。

// limitsForActor 返回该 actor 当前的发言限额。
//
// 它是本刀之前 trustForActor 的收编版本（D2）：**五道判据逐条保留**，只把
// 「年龄两态」换成「等级」。少任何一条都会造出「昨天注册的新版主被新人限流」
// 这类事故（T15）。
func limitsForActor(ctx context.Context, trust TrustPolicyResolver, actor identity.Actor) trustLimits {
	// 判据 1：未注入信任策略或无注册时间时不限制。
	if trust == nil || actor.CreatedAt.IsZero() {
		return trustLimits{}
	}
	// 判据 2：机器人不进入人类信任阶梯：它没有登录凭据、发言由 AI 配额与审核闸门
	// 约束，而「新人禁止外链」会让它无法引用站内内容——那正是回复工具存在的意义。
	// （来源：AI 辅助平台计划 M3 的既有决定；M3 收编必须原样保留。）
	if actor.Kind.IsBot() {
		return trustLimits{}
	}
	// 判据 3：super_admin / 具备管理权限的用户跳过新人限制。
	if actor.Can(identity.PermissionSettingsManage) || actor.Can(identity.PermissionTopicEditAny) {
		return trustLimits{}
	}

	// 判据 4：等级已解析 → 按 level 取限额（TL0 用新人七键，TL1+ 用站点默认 + 覆盖）。
	if actor.TrustLevelResolved {
		if actor.TrustLevel <= 0 {
			return newUserLimits(ctx, trust)
		}
		return levelLimits(ctx, trust, actor.TrustLevel)
	}

	// 判据 5：未解析（features.trust_levels 关闭 / user_activity 读失败）→ 回落到
	// 本刀之前的年龄两态语义。F 组断言的判据就在这里。
	days, err := trust.NewUserTrustDays(ctx)
	if err != nil || days <= 0 {
		return trustLimits{}
	}
	if time.Since(actor.CreatedAt) > time.Duration(days)*24*time.Hour {
		return trustLimits{}
	}
	return newUserLimits(ctx, trust)
}

// newUserLimits 是 TL0 / 未解析路径的限额：沿用 trust.new_user_* 现值。
//
// forbid_attachments 不在这里接线（D20）：options 里有键、但全仓没有执行点，
// 接线会改变运行时行为并破坏 F 组基线，另开一刀。
func newUserLimits(ctx context.Context, trust TrustPolicyResolver) trustLimits {
	topicCooldown, _ := trust.NewUserTopicCooldownSeconds(ctx)
	commentCooldown, _ := trust.NewUserCommentCooldownSeconds(ctx)
	dailyTopic, _ := trust.NewUserDailyTopicLimit(ctx)
	dailyComment, _ := trust.NewUserDailyCommentLimit(ctx)
	forbidLinks, _ := trust.NewUserForbidOutboundLinks(ctx)
	return trustLimits{
		active:          true,
		topicCooldown:   topicCooldown,
		commentCooldown: commentCooldown,
		dailyTopic:      dailyTopic,
		dailyComment:    dailyComment,
		forbidLinks:     forbidLinks,
	}
}

// levelLimits 是 TL1+ 的限额：站点默认 + 该档的显式覆盖（D27）。
//
// 读失败时返回零值 = 完全沿用站点默认：限额不是权限，读不到配置时不该把人锁死，
// 也不该放宽（站点默认就是今天的基线）。
func levelLimits(ctx context.Context, trust TrustPolicyResolver, level int) trustLimits {
	overrides, err := trust.LevelLimits(ctx, level)
	if err != nil {
		return trustLimits{}
	}
	return trustLimits{
		active:                  true,
		overrideTopicCooldown:   overrides.TopicCooldownSeconds,
		overrideCommentCooldown: overrides.CommentCooldownSeconds,
		overrideDailyTopic:      overrides.DailyTopicLimit,
		overrideDailyComment:    overrides.DailyCommentLimit,
	}
}

// resolveLimit 把「站点默认 + 新人收紧值 + 分档显式覆盖」合成最终限额。
//
// 显式覆盖优先（运营在后台把某档配成免冷却就该生效，不能被「更严者生效」压回去）；
// 其次才是新人阶梯的收紧；都没有就沿用站点默认。
func resolveLimit(base, tighten int, override *int) int {
	if override != nil {
		return *override
	}
	if tighten > 0 && (base <= 0 || tighten > base) {
		return tighten
	}
	return base
}

