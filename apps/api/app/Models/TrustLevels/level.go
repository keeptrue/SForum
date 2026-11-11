package trustlevels

import "time"

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
	lastRank := policy.LastRank()
	if lastRank < LevelMember {
		// 零值/异常策略：没有可判定的档位，只能落到 TL0。解码期保证 active ≥ 1，
		// 所以这条分支只对手搓的 Policy 生效。
		return LevelNewcomer
	}
	if policy.NewUserDays > 0 && accountAge < time.Duration(policy.NewUserDays)*24*time.Hour {
		if satisfies(activity, policy.RequirementsOf(LevelMember)) {
			return LevelMember
		}
		return LevelNewcomer
	}
	for rank := lastRank; rank >= LevelMember; rank-- {
		if satisfies(activity, policy.RequirementsOf(rank)) {
			return rank
		}
	}
	return LevelMember
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
