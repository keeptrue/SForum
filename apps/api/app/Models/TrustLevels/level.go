package trustlevels

import "time"

// LevelOf 是等级判定的纯函数：输入行为统计 + 账龄 + 策略，输出 0..4。
//
// 它不读 DB、不读 options、不调 time.Now（now 由调用方换算成 accountAge 传入），
// 因此在阈值边界上可以被穷举测试。
//
// 两段式判定：
//
//  1. 账龄闸：账龄小于 NewUserDays 时最高只到 TL1。活动量足够可以提前离开 TL0，
//     但不能提前越过 TL1——这就是「trust.new_user_days = 最多当多久新成员」。
//  2. 活动闸：从高到低找第一个被满足的档位。账龄闸之外**至少** TL1：过了新人窗口
//     就不再是「新成员」，与今天年龄两态语义（age > days → 不受新人限制）一致。
//
// 策略里的阈值不要求单调：非单调配置下取「被满足的最高档」，低档的空洞不会
// 把用户卡在低等级（否则一次手滑的配置就会静默锁死一批人）。
func LevelOf(activity Activity, accountAge time.Duration, policy Policy) Level {
	if policy.NewUserDays > 0 && accountAge < time.Duration(policy.NewUserDays)*24*time.Hour {
		if satisfies(activity, policy.Levels[LevelMember]) {
			return LevelMember
		}
		return LevelNewcomer
	}
	for level := Level(LevelCount - 1); level >= LevelMember; level-- {
		if satisfies(activity, policy.Levels[level]) {
			return level
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
