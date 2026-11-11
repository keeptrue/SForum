package trustlevelscontroller

// 本文件是信任等级 wire 形状的唯一来源。改契约前先读这里。

// adminUpdateRequest 的 Config 字段就是 trust.levels 的 Config 形状（D19 单 JSON 键）。
//
// 刻意不另造 DTO：一旦 PUT 的请求体比 Config 宽松，就会出现第二个形状，
// 而「前端漏写 limits/earnable → 保存成功但策略被清空」正是要靠严格解码挡住的。
type adminUpdateRequest struct {
	Config string `json:"config"`
}

// publicPayload 是公开等级目录：五档定义 + 阈值 + 限额 + 开关状态。
type publicPayload struct {
	Levels []levelDefinition `json:"levels"`
	// NewUserDays 是「最多当多久新成员」的上限闸，来自既有 trust.new_user_days。
	NewUserDays int `json:"newUserDays"`
	// Viewer 只在已登录且等级已解析时出现：进度只给本人（D1 隐私边界）。
	Viewer *viewerTrustLevel `json:"viewer,omitempty"`
}

type levelDefinition struct {
	Level        int                     `json:"level"`
	LevelSlug    string                  `json:"levelSlug"`
	Requirements trustlevelsRequirement  `json:"requirements"`
	// Limits 只在后台读面出现：公开「某档冷却 60 秒、日限额 10 帖」等于把限流参数
	// 交给想绕过它的人。能公开的是「你做过什么」，不该公开的是「系统怎么限制你」。
	Limits *trustLevelLimits `json:"limits,omitempty"`
	Earnable     []string                `json:"earnable"`
}

// trustlevelsRequirement 与 trustlevels.Requirements 的 JSON tag 完全一致（snake_case）。
type trustlevelsRequirement struct {
	DaysVisited       int64 `json:"days_visited"`
	TopicCount        int64 `json:"topic_count"`
	CommentCount      int64 `json:"comment_count"`
	LikeReceivedCount int64 `json:"like_received_count"`
	LikeGivenCount    int64 `json:"like_given_count"`
	BookmarkCount     int64 `json:"bookmark_count"`
}

// trustLevelLimits 的 nil 字段序列化成 null = 继承站点默认（0 有语义，不能混用）。
type trustLevelLimits struct {
	TopicCooldownSeconds   *int `json:"topicCooldownSeconds"`
	CommentCooldownSeconds *int `json:"commentCooldownSeconds"`
	DailyTopicLimit        *int `json:"dailyTopicLimit"`
	DailyCommentLimit      *int `json:"dailyCommentLimit"`
}

// viewerTrustLevel 是**本人**视角：等级 + 下一档 + 瓶颈指标。
//
// 他人视角（资料页）只有 Level/LevelSlug，绝不带 progress/bottleneck：
// like_given_count 与 bookmark_count 是刻意不公开的计数。
type viewerTrustLevel struct {
	Level      int                   `json:"level"`
	LevelSlug  string                `json:"levelSlug"`
	NextLevel  int                   `json:"nextLevel,omitempty"`
	Bottleneck *trustLevelBottleneck `json:"bottleneck,omitempty"`
}

type trustLevelBottleneck struct {
	Metric    string `json:"metric"`
	Current   int64  `json:"current"`
	Threshold int64  `json:"threshold"`
}

// adminPayload 是后台读面：整份配置 + 可勾选白名单 + 推荐默认。
type adminPayload struct {
	Config      string            `json:"config"`
	Levels      []levelDefinition `json:"levels"`
	Eligible    []string          `json:"eligiblePermissions"`
	Recommended string            `json:"recommendedConfig"`
}
