package trustlevelscontroller

import "encoding/json"

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
	Levels []publicLevelDefinition `json:"levels"`
	// NewUserDays 是「最多当多久新成员」的上限闸，来自既有 trust.new_user_days。
	NewUserDays int `json:"newUserDays"`
	// Viewer 只在已登录且等级已解析时出现：进度只给本人（D1 隐私边界）。
	Viewer *viewerTrustLevel `json:"viewer,omitempty"`
}

// publicLevelDefinition 是**公开面唯一**的档位形状：结构上没有 `limits`，也没有
// `rule`。
//
// 为什么必须与后台面拆成两个类型（而不是在共用结构上挂 omitempty）：字段一旦进了
// 共用结构，公开面的唯一保护就是「投影时别忘了不赋值」，而 `buildPayload` 的历史
// 已经证明这种纪律会被下一个改 `includeLimits` 的人顺手破坏——后台字段当时根本没
// 有类型边界，只有一条「记得置 nil」的运行期约定。拆开之后，公开面**没有** Rule 字段
// 可赋值，漏泄在编译期就不可能。
//
// wire 影响面：公开面 JSON 与拆之前逐字相同（`limits` 本来就在公开面恒 nil +
// omitempty，拆掉后不会少字段）。
type publicLevelDefinition struct {
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
	// Earnable 是该档**恰好**解锁的权限键。
	Earnable []string `json:"earnable"`
	// Retired 在目录里恒为 false：停用档位不进目录（判定与 UI 跳过）。
	Retired bool `json:"retired"`
	// Editable 报告该条是否对应 trust.levels 里可编辑的档位。TL0 恒为 false。
	Editable bool `json:"editable"`
}

// adminLevelDefinition 是后台读面的档位形状：公开形状 + 两个只有运营才该看到的字段。
//
// 匿名字段在 JSON 里被提升（flatten），所以后台响应仍是「一条档位一个对象」，
// 前端不需要改成 `level.definition.level` 这种访问路径。
type adminLevelDefinition struct {
	publicLevelDefinition
	// Limits 只在后台读面出现：公开「某档冷却 60 秒、日限额 10 帖」等于把限流参数
	// 交给想绕过它的人。能公开的是「你做过什么」，不该公开的是「系统怎么限制你」。
	Limits *trustLevelLimits `json:"limits,omitempty"`
	// Rule 只在后台读面出现：公开规则树等于把「怎么刷等级」的判定逻辑印在公告板上，
	// 而且 AST 词汇（children/cmp/params）本就不该出现在公开契约里。
	//
	// 用 `json.RawMessage` 直传：未编辑的规则必须原样回给前端，重新序列化会重排键序。
	// 无规则时省略（`omitempty`），前端按「缺键 = 无规则」处理。
	Rule json.RawMessage `json:"rule,omitempty"`
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
	// Conditions 是「离下一档还差哪几项」的**逐条句子**（只在下一档配了扁平规则时出现）。
	//
	// 与 Bottleneck 并存：Bottleneck 是数值快筛里第一项没达标的指标，Conditions 是规则树
	// 的逐条清单。两者都不含 AST 词汇（见 viewerCondition）。
	Conditions []viewerCondition `json:"conditions,omitempty"`
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
	Config   string                 `json:"config"`
	Levels   []adminLevelDefinition `json:"levels"`
	Eligible []string               `json:"eligiblePermissions"`
	// EarnableStates 标明每个可挣得键的当前状态（配置里出现的键 ∪ 当前可授予的键）。
	//   - valid    = 核心键，或某个已启用插件声明为 trustLevelEarnable；
	//   - inactive = 声明它的插件已停用/仅安装：**不授予、但允许保存**（D22 用词，不是 orphan）；
	//   - unknown  = 从未被任何插件声明：写路径会 422 `earnable_not_declared`。
	EarnableStates []earnableState `json:"earnableStates"`
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

// ---- 条件目录（GET /admin/trust-levels/conditions）----

// trustLevelConditionCatalog 是编辑器的条件目录：12 条可用条件 + 两类候选。
//
// 「12 条」是**服务端裁剪后的子集**（共享目录 14 条排除内容质量两条），不是共享目录
// 本身：把 content.pinned/content.hidden 下发给编辑器，等于让运营拖进一条保存必被拒
// 的条件（rule_condition_not_available），那是最差的可发现性——点得进去、存不下来。
type trustLevelConditionCatalog struct {
	Conditions     []trustLevelCondition `json:"conditions"`
	FactCandidates []factCandidate       `json:"factCandidates"`
	RefCandidates  []refCandidateGroup   `json:"refCandidates"`
}

// trustLevelConditionParam 是一个条件参数。字段名与徽章编辑器契约逐字一致
// （前端同一套 `badgeRule` 渲染逻辑直接复用，另起一套名字会让它读不到候选）。
type trustLevelConditionParam struct {
	Name     string   `json:"name"`
	Kind     string   `json:"kind"`
	Required bool     `json:"required"`
	Min      *int64   `json:"min,omitempty"`
	Max      *int64   `json:"max,omitempty"`
	Options  []string `json:"options,omitempty"`
	RefKind  string   `json:"refKind,omitempty"`
}

// trustLevelCondition 是一条可用条件。双语 label/describe/unit 都是服务端下发：
// 它们不进 locale JSON（条件目录本来就不该由前端 `t()` 拼）。
type trustLevelCondition struct {
	Key        string                     `json:"key"`
	ValueKind  string                     `json:"valueKind"`
	Compare    []string                   `json:"compare"`
	LabelZh    string                     `json:"labelZh"`
	LabelEn    string                     `json:"labelEn"`
	DescribeZh string                     `json:"describeZh,omitempty"`
	DescribeEn string                     `json:"describeEn,omitempty"`
	UnitZh     string                     `json:"unitZh,omitempty"`
	UnitEn     string                     `json:"unitEn,omitempty"`
	Params     []trustLevelConditionParam `json:"params"`
}

// factCandidate 是插件事实候选。除 unit 外都没有 omitempty：键在值空会让编辑器按
// undefined 渲染，而空数组在这里是合法且有意义的取值。
//
// label/unit 复用 `trustLevelName`（同一对 Zh/En）：另造一个形状相同的类型只会让
// 前端的「取当前语言」helper 需要两份重载。
type factCandidate struct {
	Key      string          `json:"key"`
	Label    trustLevelName  `json:"label"`
	Unit     *trustLevelName `json:"unit"`
	Compare  []string        `json:"compare"`
	Variants []factVariant   `json:"variants"`
}

type factVariant struct {
	Value string         `json:"value"`
	Label trustLevelName `json:"label"`
}

// refCandidate 是 objectRef 的一个下拉候选。
type refCandidate struct {
	Value string   `json:"value"`
	Label string   `json:"label"`
	Flags []string `json:"flags"`
}

// refCandidateGroup 是一类候选：五类恒五项、固定顺序，由领域层保证。
// 三个字段都没有 omitempty（`flags: []` 与 `unavailable: false` 都必须显式出现）。
type refCandidateGroup struct {
	Kind        string         `json:"kind"`
	Candidates  []refCandidate `json:"candidates"`
	Unavailable bool           `json:"unavailable"`
	Truncated   bool           `json:"truncated"`
}

// ---- 影响估算（POST /admin/trust-levels/rule-impact）----

// ruleImpactRequest 是草稿档位列表：新档也估，所以 tierId 可以是空串（不依赖已保存的 id）。
type ruleImpactRequest struct {
	Tiers []ruleImpactTierInput `json:"tiers"`
}

type ruleImpactTierInput struct {
	// TierID 只用于把结果对回请求（新档时为空串）。
	TierID string `json:"tierId"`
	// Requirements 是本档的数值快筛；省略按全 0 处理（= 不设数值门槛）。
	Requirements *trustlevelsRequirement `json:"requirements"`
	// Rule 是草稿规则树；省略/null = 该档无规则。
	Rule json.RawMessage `json:"rule"`
}

// ruleImpactResponse 与请求同序返回，逐档一条结论。
type ruleImpactResponse struct {
	Tiers []ruleImpactTierResult `json:"tiers"`
}

// ruleImpactTierResult 是一档的估算结论。
//
// `estimated: null` 与 `partial` 是**两个独立维度**，不可互相替代：
//   - null  = 算不出来（OR/NOT 里含不可估条件，或规则解不开）；
//   - partial = 算出来了，但只用了部分条件，所以那个数是上界。
type ruleImpactTierResult struct {
	TierID                string   `json:"tierId"`
	Estimated             *int64   `json:"estimated"`
	Method                string   `json:"method"`
	Partial               bool     `json:"partial"`
	UnsupportedConditions []string `json:"unsupportedConditions"`
}

// viewerCondition 是公开面「还差哪几项」里的一条。
//
// 只有 `satisfied` 与双语句子：**没有条件 key、没有参数、没有比较符**。给终端用户看的
// 是「还差哪一类事」，把 `activity.count{metric:"topic_count"} >= 3` 的形状发出去等于
// 把判定逻辑印在公告板上（验收 6 的值级断言钉的就是这个）。
type viewerCondition struct {
	Satisfied bool           `json:"satisfied"`
	Text      trustLevelName `json:"text"`
}

// earnableState 是一个可挣得权限键的当前状态（D22：valid / inactive / unknown）。
//
// 后台据此把「已停用的插件键」标灰而不是清掉：运营需要看见「这枚勾选还在，只是
// 插件停了」，否则一次停用会看起来像配置丢失。
type earnableState struct {
	Key   string `json:"key"`
	State string `json:"state"`
}
