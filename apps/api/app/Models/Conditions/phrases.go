package conditions

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/keeptrue/sforum/apps/api/app/Support/RuleEngine"
)

// 本文件是 14 种条件的「运营口吻句子」。它是**唯一实现**：公开面与后台预览端点
// 都只走这里（经 `BuildRuleRequirementText`）。
//
// 三条硬约束（测试逐条钉住）：
//   - 每种条件都必须有句子；新增条件不写 `Phrase` 由注册测试判红。
//   - 句子永远非空：参数缺失/类型不对时回落到「条件名 + 阈值」这类完整短语，
//     绝不返回空串（空串会让公开面重新出现「没有说明」）。
//   - 句子是公开可见的：不得含 `rule`/`cmp`/`params`/`children`/`op`/`type`
//     这些 AST 词汇，也不得含 `topic_count`/`days_visited` 这类参数 slug。

// PhraseFunc 把一个叶子的参数与阈值翻成双语句子。
//
// `cmp` 为 nil 只在脏规则里出现（布尔条件本来就没有 cmp）；实现必须兜住。
type PhraseFunc func(params map[string]any, cmp *ruleengine.Compare, names RefNames) (zh, en string)

// metricPhrases 是六个活跃指标的运营口吻名字。
var metricPhrases = map[string][2]string{
	MetricTopicCount:        {"发布主题", "topics published"},
	MetricCommentCount:      {"发表评论", "comments posted"},
	MetricLikeReceivedCount: {"收到赞", "likes received"},
	MetricLikeGivenCount:    {"给出赞", "likes given"},
	MetricBookmarkCount:     {"收藏内容", "bookmarks"},
	MetricDaysVisited:       {"访问天数", "days visited"},
}

// authoringScopeEn 是 `object.authoring` 的英文名词表。
//
// 名词必须来自**文案表**，不能把参数取值（`category` / `tag` 这两个裸 slug）直接拼进
// 句子：两者拼出来看着一样，但来源不同——一个是给读者看的词，一个是规则参数。
// 中文侧用「分类/标签」两个字面词，同一份口径。
var authoringScopeEn = map[string]string{
	TargetKindCategory: "in category",
	TargetKindTag:      "in tag",
}

// engagementPhrases 是三个互动动作的动词短语。
var engagementPhrases = map[string][2]string{
	ActionLike:     {"点赞过", "liked"},
	ActionBookmark: {"收藏过", "bookmarked"},
	ActionComment:  {"评论过", "commented on"},
}

// thresholdText 把比较子句翻成 `≥ 10`。缺 cmp 时返回空串（由调用方拼完整句子）。
func thresholdText(cmp *ruleengine.Compare) string {
	if cmp == nil {
		return ""
	}
	symbol := ""
	switch cmp.Op {
	case ruleengine.CompareGTE:
		symbol = "≥"
	case ruleengine.CompareLTE:
		symbol = "≤"
	case ruleengine.CompareGT:
		symbol = ">"
	case ruleengine.CompareLT:
		symbol = "<"
	case ruleengine.CompareEQ:
		symbol = "="
	default:
		return ""
	}
	return fmt.Sprintf("%s %d", symbol, cmp.Value)
}

// windowParams 读窗口参数，返回 (是否带窗口, 天数, 是否全时段)。
func windowParams(params map[string]any) (hasWindow bool, days int64, allTime bool) {
	window, ok := params["window"].(map[string]any)
	if !ok {
		return false, 0, false
	}
	kind, _ := window["kind"].(string)
	switch kind {
	case ruleengine.WindowKindAll:
		return true, 0, true
	case ruleengine.WindowKindRecentDays:
		if value, ok := paramInt64(window["days"]); ok && value > 0 {
			return true, value, false
		}
		return true, 0, false
	default:
		return false, 0, false
	}
}

func paramInt64(raw any) (int64, bool) {
	switch value := raw.(type) {
	case int:
		return int64(value), true
	case int64:
		return value, true
	case float64:
		if value != float64(int64(value)) {
			return 0, false
		}
		return int64(value), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func paramString(params map[string]any, name string) string {
	value, _ := params[name].(string)
	return strings.TrimSpace(value)
}

// windowPhraseZh 返回中文窗口前缀（全时段「累计」、最近「最近 N 天内」）。
func windowPhraseZh(params map[string]any) string {
	hasWindow, days, allTime := windowParams(params)
	if !hasWindow {
		return ""
	}
	if allTime {
		return "累计"
	}
	if days > 0 {
		return fmt.Sprintf("最近 %d 天内", days)
	}
	return ""
}

// windowPhraseEn 返回英文窗口后缀（全时段无后缀、最近「in the last N days」）。
func windowPhraseEn(params map[string]any) string {
	hasWindow, days, allTime := windowParams(params)
	if !hasWindow || allTime || days <= 0 {
		return ""
	}
	return fmt.Sprintf(" in the last %d days", days)
}

// phraseActivityCount：`activity.count`。
func phraseActivityCount(params map[string]any, cmp *ruleengine.Compare, _ RefNames) (string, string) {
	metric := paramString(params, "metric")
	labels, ok := metricPhrases[metric]
	if !ok {
		// 未知指标：不能说英文 slug，用条件自己的名字兜住。
		labels = [2]string{"指标活跃", "activity count"}
	}
	zh := strings.TrimSpace(windowPhraseZh(params) + labels[0] + " " + thresholdText(cmp))
	en := strings.TrimSpace(labels[1] + " " + thresholdText(cmp) + windowPhraseEn(params))
	return zh, en
}

// phraseActivityStreak：`activity.streak`。阈值在 cmp 上，minDays 只是镜像参数。
func phraseActivityStreak(_ map[string]any, cmp *ruleengine.Compare, _ RefNames) (string, string) {
	threshold := thresholdText(cmp)
	return strings.TrimSpace("连续访问 " + threshold + " 天"),
		strings.TrimSpace("visit streak " + threshold + " days")
}

// phraseObjectEngagement：`object.engagement`（点赞/收藏/评论过指定主题）。
func phraseObjectEngagement(params map[string]any, _ *ruleengine.Compare, names RefNames) (string, string) {
	action := paramString(params, "action")
	verbs, ok := engagementPhrases[action]
	if !ok {
		verbs = [2]string{"互动过", "engaged with"}
	}
	topicID, _ := paramInt64(params["topicId"])
	topic := names.Topic(topicID)
	return fmt.Sprintf("%s主题「%s」", verbs[0], topic.Zh),
		fmt.Sprintf("%s the topic \"%s\"", verbs[1], topic.En)
}

// phraseObjectAuthoring：`object.authoring`（在分类/标签下创作）。
func phraseObjectAuthoring(params map[string]any, cmp *ruleengine.Compare, names RefNames) (string, string) {
	kind := paramString(params, "targetKind")
	slug := paramString(params, "targetSlug")
	labelZh := "分类"
	name := names.Category(slug)
	if kind == TargetKindTag {
		labelZh = "标签"
		name = names.Tag(slug)
	}
	threshold := thresholdText(cmp)
	if threshold != "" {
		threshold = " " + threshold + " 篇"
	}
	enThreshold := thresholdText(cmp)
	if enThreshold != "" {
		enThreshold = " " + enThreshold
	}
	zh := strings.TrimSpace(windowPhraseZh(params) +
		fmt.Sprintf("在%s「%s」发布主题%s", labelZh, name.Zh, threshold))
	// 英文名词取自 authoringScopeEn（文案表），不是参数值本身。
	scopeEn := "in"
	if phrase, ok := authoringScopeEn[kind]; ok {
		scopeEn = phrase
	}
	en := strings.TrimSpace(fmt.Sprintf("topics published %s \"%s\"%s", scopeEn, name.En, enThreshold) +
		windowPhraseEn(params))
	return zh, en
}

// phraseIdentityAccountAge：`identity.account_age`。
func phraseIdentityAccountAge(_ map[string]any, cmp *ruleengine.Compare, _ RefNames) (string, string) {
	threshold := thresholdText(cmp)
	return strings.TrimSpace("账户年龄 " + threshold + " 天"),
		strings.TrimSpace("account age " + threshold + " days")
}

// phraseIdentityEmailVerify：`identity.email_verified`。
func phraseIdentityEmailVerify(map[string]any, *ruleengine.Compare, RefNames) (string, string) {
	return "邮箱已验证", "email verified"
}

// phraseIdentityOAuthLink：`identity.oauth_link`。
func phraseIdentityOAuthLink(params map[string]any, _ *ruleengine.Compare, names RefNames) (string, string) {
	provider := names.Provider(paramString(params, "providerId"))
	return fmt.Sprintf("已绑定「%s」登录", provider.Zh),
		fmt.Sprintf("\"%s\" sign-in linked", provider.En)
}

// phraseIdentityRole：`identity.role`。
func phraseIdentityRole(params map[string]any, _ *ruleengine.Compare, names RefNames) (string, string) {
	role := names.Role(paramString(params, "roleKey"))
	return fmt.Sprintf("拥有「%s」角色", role.Zh),
		fmt.Sprintf("holds the \"%s\" role", role.En)
}

// phraseIdentityAvatar：`identity.avatar`。
func phraseIdentityAvatar(map[string]any, *ruleengine.Compare, RefNames) (string, string) {
	return "已设置头像", "avatar set"
}

// profileFieldPhrases 是 `identity.profile` 的名词表。
//
// 与 `metricPhrases` 同一个理由：名词来自文案表，不来自参数取值。未命中时回落
// 「资料 / profile」而不是拼裸 slug——公开面不出现 `nickname` 这类取值。
//
// 中文用「所在地」而不是「地区」：与 `admin.users.location` 同一个词。
var profileFieldPhrases = map[string][2]string{
	profileFieldBio:       {"简介", "bio"},
	profileFieldSignature: {"签名", "signature"},
	profileFieldWebsite:   {"个人网站", "website"},
	profileFieldLocation:  {"所在地", "location"},
}

// phraseIdentityProfile：`identity.profile`。
func phraseIdentityProfile(params map[string]any, _ *ruleengine.Compare, _ RefNames) (string, string) {
	labels, ok := profileFieldPhrases[paramString(params, "field")]
	if !ok {
		labels = [2]string{"资料", "profile"}
	}
	return fmt.Sprintf("已填写「%s」", labels[0]),
		fmt.Sprintf("\"%s\" filled in", labels[1])
}

// phraseContentPinned：`content.pinned`。
func phraseContentPinned(_ map[string]any, cmp *ruleengine.Compare, _ RefNames) (string, string) {
	return contentCountPhrase(cmp, "主题被置顶", "topics pinned")
}

// phraseContentHidden：`content.hidden`。
func phraseContentHidden(_ map[string]any, cmp *ruleengine.Compare, _ RefNames) (string, string) {
	return contentCountPhrase(cmp, "主题被隐藏", "topics hidden")
}

func contentCountPhrase(cmp *ruleengine.Compare, zhLabel, enLabel string) (string, string) {
	threshold := thresholdText(cmp)
	zh := strings.TrimSpace(zhLabel + " " + threshold + " 篇")
	en := strings.TrimSpace(enLabel + " " + threshold)
	return zh, en
}

// phraseBadgeOwned：`badge.owned`。
func phraseBadgeOwned(params map[string]any, _ *ruleengine.Compare, names RefNames) (string, string) {
	badge := names.Badge(paramString(params, "slug"))
	return fmt.Sprintf("已拥有徽章「%s」", badge.Zh),
		fmt.Sprintf("owns the \"%s\" badge", badge.En)
}

// phraseFactCount：`fact.count`（插件事实计数，可带窗口）。
func phraseFactCount(params map[string]any, cmp *ruleengine.Compare, names RefNames) (string, string) {
	fact := names.Fact(paramString(params, "factKey"), paramString(params, "variant"))
	threshold := thresholdText(cmp)
	zh := strings.TrimSpace(windowPhraseZh(params) + fmt.Sprintf("「%s」%s", fact.Zh, threshold))
	en := strings.TrimSpace(fmt.Sprintf("\"%s\" total %s", fact.En, threshold) + windowPhraseEn(params))
	return zh, en
}
