// Package conditions 是规则引擎的条件目录与解析器：把 `ruleengine.Leaf` 翻译成
// Core 数据面上的一次查询。
//
// 依赖方向刻意是单向的：`conditions` → `Support/RuleEngine`（AST/校验）与 pgx；
// 它**不** import 任何领域存储包，所以规则引擎的求值不需要理解领域存储，
// 领域存储也不需要理解条件 SQL。
//
// 两条口径贯穿全包（与 `knowledge/modules/badges.md` 一致）：
//   - 软删内容仍计入「做过什么」（成就口径）：除 `content.hidden` 外，没有任何
//     条件按 `status` / `deleted_at` 过滤；
//   - 查询失败一律返 error，由 `ruleengine` 转成 unknown（fail-closed）。**绝不**
//     把错误吞成 0——那会让 `lte` 条件把「查不到」判成「满足」。
package conditions

import (
	"sync"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 条件 key。这些字符串会写进 `badges.rule` 的 JSON 与 HTTP 契约，改名即破坏契约。
const (
	KeyActivityCount       = "activity.count"
	KeyActivityStreak      = "activity.streak"
	KeyObjectEngagement    = "object.engagement"
	KeyObjectAuthoring     = "object.authoring"
	KeyIdentityAccountAge  = "identity.account_age"
	KeyIdentityEmailVerify = "identity.email_verified"
	KeyIdentityOAuthLink   = "identity.oauth_link"
	KeyIdentityRole        = "identity.role"
	KeyIdentityAvatar      = "identity.avatar"
	KeyIdentityProfile     = "identity.profile"
	KeyContentPinned       = "content.pinned"
	KeyContentHidden       = "content.hidden"
	KeyBadgeOwned          = "badge.owned"
	// KeyFactCount 是插件事实的 Core 侧读面（M1 只有这一个事实条件；M3 由扩展
	// 声明各自的 variant）。累计读 user_fact_counters，窗口读 badge_facts。
	KeyFactCount = "fact.count"
)

// 计数指标：与 user_activity 的六列一一对应，也是 `badges.metric` 的历史白名单。
const (
	MetricTopicCount        = "topic_count"
	MetricCommentCount      = "comment_count"
	MetricLikeReceivedCount = "like_received_count"
	MetricLikeGivenCount    = "like_given_count"
	MetricBookmarkCount     = "bookmark_count"
	MetricDaysVisited       = "days_visited"
)

// Metrics 返回六个指标的稳定顺序（HTTP 目录与测试都按它遍历）。
func Metrics() []string {
	return []string{
		MetricTopicCount, MetricCommentCount, MetricLikeReceivedCount,
		MetricLikeGivenCount, MetricBookmarkCount, MetricDaysVisited,
	}
}

// identity.profile 的四个资料项取值。
//
// `website` 是**契约字面量**，落到 `user_profiles.website_url` 列：取值与列名不同名
// 是既有契约，不要为了「对齐列名」把它改成 `website_url`（那会破坏已存下的规则）。
const (
	profileFieldBio       = "bio"
	profileFieldSignature = "signature"
	profileFieldWebsite   = "website"
	profileFieldLocation  = "location"
)

// profileFieldOptions 返回资料项的稳定顺序（= 编辑器下拉顺序）。
//
// 注册项的 `Options` 与解析器的白名单都取自这里：枚举只写一份，否则「目录里有、
// 解析器不认」这种半实现会以「保存成功、永不成立、零报错」的形式上线。
func profileFieldOptions() []string {
	return []string{profileFieldBio, profileFieldSignature, profileFieldWebsite, profileFieldLocation}
}

// 定向对象的两类目标。
const (
	TargetKindCategory = "category"
	TargetKindTag      = "tag"
)

// 互动动作。
const (
	ActionLike     = "like"
	ActionBookmark = "bookmark"
	ActionComment  = "comment"
)

// Definition 是一个条件的完整元数据。它同时喂给三处：
//   - `ruleengine.Validate`（ValueKind / Compare / Params）
//   - `GET /admin/badges/conditions`（Label*/Describe*/Unit*，M2 编辑器据此渲染）
//   - `rule_health` 的失效引用检查（Params 里的 objectRef RefKind）
//
// 文案直接写在 Go 里而不是 locale JSON：M1 阶段往 `zh-CN.json` / `en-US.json`
// 加键会撞 `adminBadgesI18n.test.ts` 的双向断言（locale 里多出来的键也判红），
// 而条件目录是**服务端下发**的数据，本来就不该走前端 `t()`。
type Definition struct {
	Key        string
	ValueKind  ruleengine.ValueKind
	Compare    []ruleengine.CompareOp
	Params     ruleengine.ParamsSchema
	LabelZh    string
	LabelEn    string
	DescribeZh string
	DescribeEn string
	UnitZh     string
	UnitEn     string
	// Phrase 把叶子翻成公开面可读的双语句子。
	//
	// **每个条件都必须有**：缺一条就是「公开面这个条件没有说明」，而手填已被移除，
	// 没有兜底来源。注册测试（`TestEveryConditionHasPhrase`）按 key 数量比对这个
	// 字段，新增条件忘了写句子会直接判红，而不是上线后显示半句话。
	Phrase PhraseFunc
}

// Registry 是 Core 内置条件目录。它实现 `ruleengine.Catalog`。
//
// 它是**只读单例**：插件事实候选不进字段（那会变成全局可变状态 + 数据竞争），
// 而是作为 `PublishableDefinitions` 的参数传入。
type Registry struct {
	defs  map[string]Definition
	order []string
}

var (
	coreOnce     sync.Once
	coreRegistry *Registry
)

// Core 返回内置条件目录（只读单例）。
func Core() *Registry {
	coreOnce.Do(func() { coreRegistry = newCoreRegistry() })
	return coreRegistry
}

// ConditionMeta 实现 ruleengine.Catalog。
func (r *Registry) ConditionMeta(key string) (ruleengine.ConditionMeta, bool) {
	def, ok := r.defs[key]
	if !ok {
		return ruleengine.ConditionMeta{}, false
	}
	return ruleengine.ConditionMeta{
		Key:            def.Key,
		ValueKind:      def.ValueKind,
		AllowedCompare: def.Compare,
		Params:         def.Params,
	}, true
}

// Definition 返回单个条件的元数据。
func (r *Registry) Definition(key string) (Definition, bool) {
	def, ok := r.defs[key]
	return def, ok
}

// Definitions 按注册顺序返回**全部**条件（校验与测试用，不过滤）。
func (r *Registry) Definitions() []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, key := range r.order {
		out = append(out, r.defs[key])
	}
	return out
}

// PublishableDefinitions 是 `GET /admin/badges/conditions` 的返回集合。
//
// 与 `Definitions` 的差别只有一条：**objectRef 参数没有候选的条件不出现**。
// M1 的 `fact.count` 就是这种——没有扩展声明 factKey 时它是空下拉，暴露出去只会
// 让运营点进死路；M3 有扩展声明候选后它自动出现。校验/求值仍然认它（`Catalog`
// 不过滤），否则 M3 存下来的规则会通不过校验。
func (r *Registry) PublishableDefinitions(candidates []FactCandidate) []Definition {
	out := make([]Definition, 0, len(r.order))
	for _, key := range r.order {
		def := r.defs[key]
		if !hasCandidates(def, candidates) {
			continue
		}
		out = append(out, withFactOptions(def, FactKeys(candidates)))
	}
	return out
}

// withFactOptions 把已声明的事实 key 填进 `factKey` 参数的 Options。
//
// 只过滤不填内容 = 数量对、下拉是空的，编辑器仍然渲染不出候选。这里必须**复制**
// specs 再改：`Core()` 是只读单例，原地写会污染校验用的全集，并在并发下产生竞争。
func withFactOptions(def Definition, factKeys []string) Definition {
	if len(factKeys) == 0 {
		return def
	}
	specs := make([]ruleengine.ParamSpec, len(def.Params.Specs))
	copy(specs, def.Params.Specs)
	changed := false
	for index := range specs {
		if specs[index].Kind == ruleengine.ParamObjectRef && specs[index].RefKind == "fact" {
			specs[index].Options = append([]string(nil), factKeys...)
			changed = true
		}
	}
	if !changed {
		return def
	}
	def.Params = ruleengine.ParamsSchema{Specs: specs}
	return def
}

// hasCandidates 判断一个条件的 objectRef 参数是否有候选来源。
func hasCandidates(def Definition, candidates []FactCandidate) bool {
	for _, spec := range def.Params.Specs {
		if spec.Kind != ruleengine.ParamObjectRef {
			continue
		}
		// 判据是「有没有候选」，不是「有没有声明」：没有候选时暴露一个空下拉，
		// 运营点进去是死路（M1 无扩展时必然为空）。
		if spec.RefKind == "fact" && len(candidates) == 0 {
			return false
		}
	}
	return true
}

func countCompare() []ruleengine.CompareOp {
	// 计数条件只允许 gte / lte：阈值条件是「达到某条线」，「恰好不等于」不是运营会
	// 表达的需求，而且会让 all/not 的组合更难读。
	return []ruleengine.CompareOp{ruleengine.CompareGTE, ruleengine.CompareLTE}
}

func windowParam() ruleengine.ParamSpec {
	return ruleengine.ParamSpec{Name: "window", Kind: ruleengine.ParamWindow, Required: true}
}

func newCoreRegistry() *Registry {
	defs := []Definition{
		{
			Key:       KeyActivityCount,
			Phrase:    phraseActivityCount,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "metric", Kind: ruleengine.ParamEnum, Required: true, Options: Metrics()},
				windowParam(),
			}},
			LabelZh: "活跃计数", LabelEn: "Activity count",
			DescribeZh: "按指标统计活跃度，可选全部时间或最近 N 天。", DescribeEn: "Activity counter by metric, all time or the last N days.",
			UnitZh: "次", UnitEn: "times",
		},
		{
			Key:       KeyActivityStreak,
			Phrase:    phraseActivityStreak,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "minDays", Kind: ruleengine.ParamInt, Required: true, Min: 1, Max: ruleengine.MaxWindowDays},
			}},
			LabelZh: "连续访问", LabelEn: "Visit streak",
			DescribeZh: "连续访问天数（以今天或昨天收尾）。", DescribeEn: "Consecutive visiting days, ending today or yesterday.",
			UnitZh: "天", UnitEn: "days",
		},
		{
			Key:       KeyObjectEngagement,
			Phrase:    phraseObjectEngagement,
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "action", Kind: ruleengine.ParamEnum, Required: true, Options: []string{ActionLike, ActionBookmark, ActionComment}},
				{Name: "topicId", Kind: ruleengine.ParamInt, Required: true, Min: 1},
			}},
			LabelZh: "与主题互动", LabelEn: "Engaged with topic",
			DescribeZh: "是否对指定主题点过赞、收藏过或评论过。", DescribeEn: "Whether the user liked, bookmarked or commented on the given topic.",
		},
		{
			Key:       KeyObjectAuthoring,
			Phrase:    phraseObjectAuthoring,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "targetKind", Kind: ruleengine.ParamEnum, Required: true, Options: []string{TargetKindCategory, TargetKindTag}},
				{Name: "targetSlug", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "category"},
				windowParam(),
			}},
			LabelZh: "在分类/标签下创作", LabelEn: "Authored in category/tag",
			DescribeZh: "在指定分类或标签下发布的主题数。", DescribeEn: "Topics authored in the given category or tag.",
			UnitZh: "篇", UnitEn: "topics",
		},
		{
			Key:       KeyIdentityAccountAge,
			Phrase:    phraseIdentityAccountAge,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			LabelZh:   "账户年龄", LabelEn: "Account age",
			DescribeZh: "账户注册至今的天数。", DescribeEn: "Days since the account was created.",
			UnitZh: "天", UnitEn: "days",
		},
		{
			Key:       KeyIdentityEmailVerify,
			Phrase:    phraseIdentityEmailVerify,
			ValueKind: ruleengine.ValueBoolean,
			LabelZh:   "邮箱已验证", LabelEn: "Email verified",
			DescribeZh: "邮箱是否已验证。", DescribeEn: "Whether the email address is verified.",
		},
		{
			Key:       KeyIdentityOAuthLink,
			Phrase:    phraseIdentityOAuthLink,
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "providerId", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "provider"},
			}},
			LabelZh: "已绑定第三方账号", LabelEn: "OAuth account linked",
			DescribeZh: "是否绑定了指定的第三方登录（只认启用中的绑定）。", DescribeEn: "Whether the given OAuth provider is linked and active.",
		},
		{
			Key:       KeyIdentityRole,
			Phrase:    phraseIdentityRole,
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "roleKey", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "role"},
			}},
			LabelZh: "拥有角色", LabelEn: "Has role",
			DescribeZh: "是否拥有指定角色（按角色 key）。", DescribeEn: "Whether the user holds the given role key.",
		},
		{
			Key:       KeyIdentityAvatar,
			Phrase:    phraseIdentityAvatar,
			ValueKind: ruleengine.ValueBoolean,
			LabelZh:   "已设置头像", LabelEn: "Avatar set",
			DescribeZh: "是否上传过头像。", DescribeEn: "Whether the user has uploaded an avatar.",
		},
		{
			Key:       KeyIdentityProfile,
			Phrase:    phraseIdentityProfile,
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "field", Kind: ruleengine.ParamEnum, Required: true, Options: profileFieldOptions()},
			}},
			LabelZh: "资料已填写", LabelEn: "Profile filled in",
			DescribeZh: "资料里的简介、签名、个人网站或所在地是否已填写。填了就算，不看内容质量。", DescribeEn: "Whether the profile bio, signature, website or location is filled in. Any content counts; quality is not judged.",
		},
		{
			Key:       KeyContentPinned,
			Phrase:    phraseContentPinned,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			LabelZh:   "主题被置顶", LabelEn: "Topics pinned",
			DescribeZh: "被置顶的主题数（含已软删）。", DescribeEn: "Number of pinned topics (soft-deleted included).",
			UnitZh: "篇", UnitEn: "topics",
		},
		{
			Key:       KeyContentHidden,
			Phrase:    phraseContentHidden,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			LabelZh:   "主题被隐藏", LabelEn: "Topics hidden",
			DescribeZh: "被隐藏的主题数。这是唯一按内容状态判定的条件，通常配合「非」使用。", DescribeEn: "Number of hidden topics. The only condition that looks at content status; usually used under NOT.",
			UnitZh: "篇", UnitEn: "topics",
		},
		{
			Key:       KeyBadgeOwned,
			Phrase:    phraseBadgeOwned,
			ValueKind: ruleengine.ValueBoolean,
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "slug", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "badge"},
			}},
			LabelZh: "已拥有徽章", LabelEn: "Owns badge",
			DescribeZh: "是否已拥有指定徽章。保存时会做循环依赖检测。", DescribeEn: "Whether the user owns the given badge. Save-time cycle detection applies.",
		},
		{
			Key:       KeyFactCount,
			Phrase:    phraseFactCount,
			ValueKind: ruleengine.ValueCount,
			Compare:   countCompare(),
			Params: ruleengine.ParamsSchema{Specs: []ruleengine.ParamSpec{
				{Name: "factKey", Kind: ruleengine.ParamObjectRef, Required: true, RefKind: "fact"},
				{Name: "variant", Kind: ruleengine.ParamEnum, Required: false, Options: nil},
				windowParam(),
			}},
			LabelZh: "插件事实计数", LabelEn: "Plugin fact count",
			DescribeZh: "插件上报的事实累计值，可按窗口统计。", DescribeEn: "Accumulated plugin fact total, optionally within a window.",
			UnitZh: "次", UnitEn: "times",
		},
	}

	registry := &Registry{defs: make(map[string]Definition, len(defs)), order: make([]string, 0, len(defs))}
	for _, def := range defs {
		registry.defs[def.Key] = def
		registry.order = append(registry.order, def.Key)
	}
	return registry
}
