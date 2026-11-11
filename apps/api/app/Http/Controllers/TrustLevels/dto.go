package trustlevelscontroller

// 本文件是信任等级 wire 形状的唯一来源。改契约前先读这里。

// adminUpdateRequest 的 Config 字段就是 trust.levels 的 Config 形状（D19 单 JSON 键）。
//
// 刻意不另造 DTO：一旦 PUT 的请求体比 Config 宽松，就会出现第二个形状，
// 而「前端漏写 limits/earnable → 保存成功但策略被清空」正是要靠严格解码挡住的。
type adminUpdateRequest struct {
	Config string `json:"config"`
}

// publicPayload 是公开等级目录：active 档位定义 + 阈值 + 限额 + 开关状态。
type publicPayload struct {
	// Levels 的第一条永远是合成的 TL0（editable:false），其后按 rank 递增。
	Levels []levelDefinition `json:"levels"`
	// NewUserDays 是「最多当多久新成员」的上限闸，来自既有 trust.new_user_days。
	NewUserDays int `json:"newUserDays"`
	// Viewer 只在已登录且等级已解析时出现：进度只给本人（D1 隐私边界）。
	Viewer *viewerTrustLevel `json:"viewer,omitempty"`
}

type levelDefinition struct {
	Level     int    `json:"level"`
	LevelSlug string `json:"levelSlug"`
	// DisplayName 是运营可改的双语名（D-N3）。TL0 用内置名。
	DisplayName trustLevelName `json:"displayName"`
	// Icon 是 lucide 图标名，必在管理员读面的 iconChoices 里。
	Icon string `json:"icon"`
	// Color 是该档的**有效色**：恒非空，TL0 也有。配置里的空串（= 跟随位次自动）
	// 在投影时已按 rank 折算掉，前端不需要再判断「空值算哪个颜色」。
	Color        string                 `json:"color"`
	Requirements trustlevelsRequirement `json:"requirements"`
	// Limits 只在后台读面出现：公开「某档冷却 60 秒、日限额 10 帖」等于把限流参数
	// 交给想绕过它的人。能公开的是「你做过什么」，不该公开的是「系统怎么限制你」。
	Limits *trustLevelLimits `json:"limits,omitempty"`
	// Earnable 是该档**恰好**解锁的权限键。
	Earnable []string `json:"earnable"`
	// Retired 在目录里恒为 false：停用档位不进目录（判定与 UI 跳过）。
	Retired bool `json:"retired"`
	// Editable 报告该条是否对应 trust.levels 里可编辑的档位。TL0 恒为 false。
	Editable bool `json:"editable"`
}

// trustLevelName 是 LocalizedName 的 wire 形状（两语言都必填）。
type trustLevelName struct {
	Zh string `json:"zh"`
	En string `json:"en"`
}

// trustLevelNamePool 是命名池：编辑辅助，不是存储字段（D-N3）。
// zh 与 en 一一对应、同序；前端按下标配对取值。
type trustLevelNamePool struct {
	Zh []string `json:"zh"`
	En []string `json:"en"`
}

// tierCountBounds 是 active 档位数的合法区间（D-N7）。
type tierCountBounds struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// trustLevelConfigError 是一条字段级错误明细（D-N14 的 wire 形状）。
//
// 字段名是**对外契约**：前端按 rank/tierId 定位到输入控件、按 field 高亮那一格、
// 把 reason 原样显示给运营。机器可判的码仍是响应体的 reason
// （trust_levels.invalid_config），不随档位变化——前端分流逻辑不因此变复杂。
type trustLevelConfigError struct {
	// Rank 是出错的档位序号（TL1 起）；0 表示无法定位到具体档位（全局字段或停用档位）。
	Rank int `json:"rank"`
	// TierID 是出错的档位身份；新档或全局错误时为空串。
	TierID string `json:"tierId"`
	// Field 是稳定的字段路径（name.zh / icon / requirements.days_visited / limits / ...）。
	Field string `json:"field"`
	// ReasonCode 是**稳定且可枚举**的机器码（D-N14），前端按它拼 i18n 文案。
	// 取值域见 OpenAPI 的 TrustLevelConfigErrorDetail.reasonCode enum，
	// 与 trustlevels.ConfigErrorReasonCodes() 同源；改名等于破坏兼容。
	ReasonCode string `json:"reasonCode"`
	// Params 是 i18n 插值参数（图标名、指标名、长度上限、当前档数这类动态值）。
	// 键名同样是对外契约，见 OpenAPI 上的说明。
	Params map[string]any `json:"params,omitempty"`
	// Reason 是人读兜底（中文散文）：只用于服务端日志与「没有该 reasonCode 文案的
	// 旧前端」。**不用于正式展示**——后端不输出文案是既有的 i18n 口径。
	Reason string `json:"reason"`
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
// 他人视角（资料页）只有 Level/LevelSlug/DisplayName，绝不带 progress/bottleneck：
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

// adminPayload 是后台读面：整份配置 + 可勾选白名单 + 推荐默认 + 编辑器元数据。
type adminPayload struct {
	// Config 是**规范化后的 v2 投影**（DecodeConfig → EncodeConfig），不是库里的
	// 原始字节：v1 值已升级、派生 id 已落成字面量、可选字段的省略/空值已抹平。
	// 因此它只保证语义等价；需要逐字节比对库值时必须直查 option。
	Config   string            `json:"config"`
	Levels   []levelDefinition `json:"levels"`
	Eligible []string          `json:"eligiblePermissions"`
	// Baseline 是「系统基线门」的键（Z3-b″）：默认安装下 TL0 必须被扣掉、
	// TL1 起自动生效的那几个键。与 Eligible 同级但语义相反——
	// Eligible 回答「允许被等级授予什么」，Baseline 回答「TL0 会被扣掉什么」。
	Baseline    []string `json:"baselinePermissions"`
	Recommended string   `json:"recommendedConfig"`
	// IconChoices 是服务端接受的图标白名单（D-N4）。顺序即 UI 展示顺序，
	// 前 5 项同时是默认图标序列（芽/叶/屋/山/冠 + 星）。
	IconChoices []string `json:"iconChoices"`
	// ColorChoices 是服务端接受的颜色白名单。顺序即默认色序（第 n 项属于 rank n，
	// TL0 = slate），所以前端同时用它做下拉选项与「跟随位次」的预览；不得自己抄一份。
	ColorChoices []string `json:"colorChoices"`
	// NamePool 是取名辅助池（D-N3），不落存储；运营可完全无视它自己写名字。
	NamePool trustLevelNamePool `json:"namePool"`
	// TierLimits 是 active 档位数的合法区间（D-N7）。
	TierLimits tierCountBounds `json:"tierLimits"`
	// NextTierSeq 是下一个新档 id 的序号（D-N6）：前端新建档位时不得自己编 id，
	// 这里下发的序号只是给 UI 显示「将创建 tier_<n>」用，最终由服务端派生。
	NextTierSeq int `json:"nextTierSeq"`
}
