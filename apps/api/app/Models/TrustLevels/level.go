package trustlevels

import (
	"context"
	"encoding/json"
	"time"
)

// LevelOf 是等级判定的纯函数：输入行为统计 + 账龄 + 策略，输出 0..policy.LastRank()。
//
// 它不读 DB、不读 options、不调 time.Now（now 由调用方换算成 accountAge 传入），
// 因此在阈值边界上可以被穷举测试。
//
// 两段式判定：
//
//  1. 账龄闸：账龄小于 NewUserDays 时最高只到 TL1。活动量足够可以提前离开 TL0，
//     但不能提前越过 TL1——这就是「trust.new_user_days = 最多当多久新成员」。
//  2. 活动闸：从**最高 active 档**往低找第一个被满足的档位。账龄闸之外**至少** TL1：
//     过了新人窗口就不再是「新成员」，与今天年龄两态语义一致。
//
// 策略里的阈值不要求单调：非单调配置下取「被满足的最高档」，低档的空洞不会
// 把用户卡在低等级（否则一次手滑的配置就会静默锁死一批人）。
//
// 判定只走 active 档位：retired 档位既不占 rank 也不参与比较（D-N2），所以停用一档
// 等于让它消失，恢复一档等于回到它原来的 rank。
func LevelOf(activity Activity, accountAge time.Duration, policy Policy) Level {
	// 薄包装：**只有一份遍历实现**（`levelOfWithRules`）。复制第二份比较逻辑是徽章
	// 那刀「两份 deny CTE」的同形风险——副本会漂移，而漂移时测试往往只覆盖一份。
	level, _, _ := levelOfWithRules(context.Background(), activity, accountAge, policy, nil)
	return level
}

// LevelOfWithRules 是带规则求值的判定：数值快筛 AND 可选规则树。
//
// `eval == nil` 时与 `LevelOf` 逐字节等价（同一条遍历、同一个返回）。
//
// **fail-closed**：某一档配了规则但求值器缺位 / 求值判不了（unknown）→ 该档**不成立**，
// 继续往低档找。绝不因为「规则没求出来」就把它当成成立——那会把一次查询故障变成
// 全站用户升级，且没有任何日志会被注意到。
func LevelOfWithRules(
	ctx context.Context,
	activity Activity,
	accountAge time.Duration,
	policy Policy,
	eval RuleEvaluator,
) Level {
	level, _, _ := levelOfWithRules(ctx, activity, accountAge, policy, eval)
	return level
}

// levelOfWithRules 是唯一遍历实现，额外返回「本次是否不完整」与规则问题清单。
//
// 拆出内部版本的原因：`Evaluate()` 需要 `Incomplete` 与逐条问题（M4 的事件抑制与
// 进度读面要用），而 `LevelOf` 只需要一个整数。两者的**判定逻辑必须是同一份**，
// 所以只有这一个函数体。
func levelOfWithRules(
	ctx context.Context,
	activity Activity,
	accountAge time.Duration,
	policy Policy,
	eval RuleEvaluator,
) (Level, bool, []error) {
	lastRank := policy.LastRank()
	if lastRank < LevelMember {
		// 零值/异常策略：没有可判定的档位，只能落到 TL0。解码期保证 active ≥ 1，
		// 所以这条分支只对手搓的 Policy 生效。
		return LevelNewcomer, false, nil
	}
	// 数值预筛 + 合成根 Prepare 一次（D19）。
	//
	// 顺序是**先预筛、后 Prepare**：只把「数值已过 且 带规则」的档位装进合成根。
	// 于是「某档数值不过」时，它独有的条件类型**一条 SQL 都不会产生**——这是 D19-c
	// 的探针判据，也是「数值不过不查规则」这条语义的可观测形态。
	incomplete := false
	problems := make([]error, 0, 1)
	if eval != nil {
		pending := make([]json.RawMessage, 0, 4)
		for rank := LevelMember; rank <= lastRank; rank++ {
			if !satisfies(activity, policy.RequirementsOf(rank)) {
				continue
			}
			if rule := policy.RuleOf(rank); len(rule) > 0 {
				pending = append(pending, rule)
			}
		}
		if len(pending) > 0 {
			eval.Prepare(ctx, pending)
		}
	}

	// 逐档求值：数值不过直接跳过（不构造、不查询），数值过且无规则即成立，
	// 数值过且有规则则求值，成立即返回、不成立继续降档。
	evaluate := func(rank Level) bool {
		rule := policy.RuleOf(rank)
		if len(rule) == 0 {
			return true
		}
		if eval == nil {
			// 配了规则却没有求值器：判不了 → 不成立（fail-closed）。
			incomplete = true
			return false
		}
		outcome := eval.Match(ctx, rule)
		if len(outcome.Problems) > 0 {
			problems = append(problems, outcome.Problems...)
		}
		if outcome.Incomplete {
			incomplete = true
		}
		return outcome.Matched
	}

	if policy.NewUserDays > 0 && accountAge < time.Duration(policy.NewUserDays)*24*time.Hour {
		// 账龄闸：规则不能突破 TL1，但 **TL1 自己的规则同样生效**（计划 §3.2 第 1 条）。
		if satisfies(activity, policy.RequirementsOf(LevelMember)) && evaluate(LevelMember) {
			return LevelMember, incomplete, problems
		}
		return LevelNewcomer, incomplete, problems
	}
	for rank := lastRank; rank >= LevelMember; rank-- {
		if !satisfies(activity, policy.RequirementsOf(rank)) {
			continue
		}
		if evaluate(rank) {
			return rank, incomplete, problems
		}
	}
	return LevelMember, incomplete, problems
}

// LevelOfAt 是 LevelOf 的时间戳包装：now 由调用方注入，本包不持有时钟。
func LevelOfAt(activity Activity, createdAt, now time.Time, policy Policy) Level {
	return LevelOf(activity, accountAge(createdAt, now), policy)
}

// satisfies 判定一份行为统计是否满足某档的全部最小阈值。
func satisfies(activity Activity, requirement Requirements) bool {
	return activity.TopicCount >= requirement.TopicCount &&
		activity.CommentCount >= requirement.CommentCount &&
		activity.LikeReceivedCount >= requirement.LikeReceivedCount &&
		activity.LikeGivenCount >= requirement.LikeGivenCount &&
		activity.BookmarkCount >= requirement.BookmarkCount &&
		activity.DaysVisited >= requirement.DaysVisited
}
