// Package trustlevels 派生用户信任等级（TL0–TL4）以及随之解锁的既有权限键。
//
// 设计约束（见 knowledge/decisions/2026-11-10-trust-levels.md）：
//
//   - 等级是纯函数派生值，不落列、不缓存。Level / LevelAt 不读 DB、不调
//     time.Now——now 由调用方注入，这样阈值边界测试不需要时钟替换。
//   - 已挣得权限是纯加性来源，永不否定静态授予；与「管理类权限」硬不相交。
//   - 本包**不 import** Identity / Options / Forum：阈值与权限键都在这里声明或由
//     调用方传入，依赖方向只有 Identity → TrustLevels。
package trustlevels

import "time"

// Level 是信任等级。零值是 TL0（新成员），与 Actor 零值语义一致。
type Level int

const (
	LevelNewcomer Level = 0
	LevelMember   Level = 1
	LevelRegular  Level = 2
	LevelVeteran  Level = 3
	LevelLeader   Level = 4

	// LevelCount 是等级总数，用于数组长度与遍历上界。
	LevelCount = 5
)

// Valid 报告等级是否落在 0..LevelCount-1。
func (l Level) Valid() bool { return l >= LevelNewcomer && l < LevelCount }

// nameSlugs 是等级到稳定标识的映射；i18n 键由它拼出，后端不输出文案。
var nameSlugs = [LevelCount]string{
	"newcomer",
	"member",
	"resident",
	"veteran",
	"leader",
}

// Slug 返回等级的稳定标识（用于契约与后台表单字段名）。
func (l Level) Slug() string {
	if !l.Valid() {
		return ""
	}
	return nameSlugs[l]
}

// Activity 是等级判定的行为输入，全部来自 user_activity 单行（days_visited 的真值
// 在 user_visits，但同样由统计层维护）。阅读类指标不参与判定。
type Activity struct {
	TopicCount        int64
	CommentCount      int64
	LikeReceivedCount int64
	LikeGivenCount    int64
	BookmarkCount     int64
	DaysVisited       int64
}

// Requirements 是一档等级的最小行为量，AND 语义：零值字段表示该指标不设门槛。
// JSON 字段名与 Metrics 标识严格一致：后台表单、契约、option JSON 三处共用一份
// 命名，改这里等于改三处（DisallowUnknownFields 会把漏改直接变成解码错误）。
type Requirements struct {
	DaysVisited       int64 `json:"days_visited"`
	TopicCount        int64 `json:"topic_count"`
	CommentCount      int64 `json:"comment_count"`
	LikeReceivedCount int64 `json:"like_received_count"`
	LikeGivenCount    int64 `json:"like_given_count"`
	BookmarkCount     int64 `json:"bookmark_count"`
}

// Metrics 是 Requirements 六个字段的稳定标识，也就是 trust.levels 这个**单 JSON
// 键**里 `levels[].requirements` 的字段名（见 config.go 的 Config/LevelConfig）。
// 顺序即后台表单与契约里的展示顺序。
//
// 注意：这里**不是** options 键后缀。D19 之前的「每档每指标一个扁平键
// trust.level.<n>.<metric>」方案已作废。
var Metrics = []string{
	"days_visited",
	"topic_count",
	"comment_count",
	"like_received_count",
	"like_given_count",
	"bookmark_count",
}

// Value 按 metric 标识读一个阈值；未知 metric 返回 0,false。
func (r Requirements) Value(metric string) (int64, bool) {
	switch metric {
	case "days_visited":
		return r.DaysVisited, true
	case "topic_count":
		return r.TopicCount, true
	case "comment_count":
		return r.CommentCount, true
	case "like_received_count":
		return r.LikeReceivedCount, true
	case "like_given_count":
		return r.LikeGivenCount, true
	case "bookmark_count":
		return r.BookmarkCount, true
	}
	return 0, false
}

// SetValue 按 metric 标识写一个阈值；未知 metric 返回 false。
func (r *Requirements) SetValue(metric string, value int64) bool {
	if r == nil {
		return false
	}
	switch metric {
	case "days_visited":
		r.DaysVisited = value
	case "topic_count":
		r.TopicCount = value
	case "comment_count":
		r.CommentCount = value
	case "like_received_count":
		r.LikeReceivedCount = value
	case "like_given_count":
		r.LikeGivenCount = value
	case "bookmark_count":
		r.BookmarkCount = value
	default:
		return false
	}
	return true
}

// Value 按 metric 标识读一项行为统计；未知 metric 返回 0,false。
func (a Activity) Value(metric string) (int64, bool) {
	switch metric {
	case "days_visited":
		return a.DaysVisited, true
	case "topic_count":
		return a.TopicCount, true
	case "comment_count":
		return a.CommentCount, true
	case "like_received_count":
		return a.LikeReceivedCount, true
	case "like_given_count":
		return a.LikeGivenCount, true
	case "bookmark_count":
		return a.BookmarkCount, true
	}
	return 0, false
}

// IsZero 报告该档是否完全没有门槛（用于识别「这一档永远不会被达到」的配置错误）。
func (r Requirements) IsZero() bool {
	return r == Requirements{}
}

// LevelLimits 是一档等级的发言限额。nil 表示「沿用站点默认」。
//
// 指针而不是 int：0 在这里是**有语义的**（免冷却 = 冷却 0 秒），不能与
// 「没配」混为一谈。限额只影响多频繁，不进 Can()（D8）。
type LevelLimits struct {
	TopicCooldownSeconds   *int `json:"topicCooldownSeconds"`
	CommentCooldownSeconds *int `json:"commentCooldownSeconds"`
	DailyTopicLimit        *int `json:"dailyTopicLimit"`
	DailyCommentLimit      *int `json:"dailyCommentLimit"`
}

// Policy 是等级判定的完整策略。
type Policy struct {
	// NewUserDays 是「最多当多久新成员」的上限闸：账龄不足时最高只到 TL1。
	// 0 表示不设闸（任何人至少 TL1）。
	NewUserDays int
	// Levels[n] 是达到 TLn 所需的最小行为量；Levels[0]（TL0）不参与判定。
	Levels [LevelCount]Requirements
	// Limits[n] 是 TLn 的发言限额（nil 字段沿用站点默认）。
	Limits [LevelCount]LevelLimits
	// Earnable 是「等级 → 解锁的既有权限键」映射，默认空集（D17）。
	Earnable Earnable
}

// accountAge 把「注册时刻 → 账龄」的换算收在一处，避免各调用方各写一份。
func accountAge(createdAt, now time.Time) time.Duration {
	if createdAt.IsZero() {
		// 未知注册时间按「刚注册」处理：最严，不会因为读失败而放宽限制。
		return 0
	}
	return now.Sub(createdAt)
}
