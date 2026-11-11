package trustlevels

// DefaultPolicy 是内置推荐阈值（运营可在后台覆盖，并支持一键恢复）。
//
// 取值原则：**保守**。默认 earnable 是**空集**（D17）：`SeedMemberPermissions` 已经
// 给了默认成员全部非管理内容键，所以「TL2 解锁 topic.create」对默认安装是零行为
// 变化的假功能。等级的真实默认价值是分档限额 + 等级展示 + 进度提示。
//
// NewUserDays 的默认值与 options 的 trust.new_user_days 一致（7 天）；decode 时
// 会被 option 里的值覆盖，这里只是兜底。
func DefaultPolicy() Policy {
	return Policy{
		NewUserDays: 7,
		Levels: [LevelCount]Requirements{
			// TL0 是兜底档：不满足 TL1 即落 TL0。
			LevelNewcomer: {},
			// TL1 保守：不要求发主题、不要求点赞（点赞依赖别人的行为，小站会卡死），
			// 只要求「跨 3 天回来 + 5 条回复」。见 capability-map.md §5.2。
			LevelMember: {
				DaysVisited:  3,
				CommentCount: 5,
			},
			LevelRegular: {
				DaysVisited:       15,
				TopicCount:        2,
				CommentCount:      20,
				LikeReceivedCount: 5,
				LikeGivenCount:    3,
			},
			LevelVeteran: {
				DaysVisited:       45,
				TopicCount:        8,
				CommentCount:      80,
				LikeReceivedCount: 30,
				LikeGivenCount:    20,
				BookmarkCount:     5,
			},
			LevelLeader: {
				DaysVisited:       100,
				TopicCount:        25,
				CommentCount:      250,
				LikeReceivedCount: 100,
				LikeGivenCount:    80,
				BookmarkCount:     20,
			},
		},
		Limits: [LevelCount]LevelLimits{
			// TL0 由 trust.new_user_* 七键决定，这里不重复配置。
			LevelNewcomer: {},
			// TL1 起沿用站点默认冷却/日限额（nil = 继承）。
			LevelMember:  {},
			LevelRegular: {},
			// TL1–TL4 默认**全部继承站点默认**（D27）：放宽必须由运营在后台显式配，
			// 代码里不发明「高等级绕过站级冷却」这种看不见也关不掉的行为。
			LevelVeteran: {},
			LevelLeader:  {},
		},
	}
}
