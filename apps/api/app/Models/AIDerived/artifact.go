// Package aiderived 实现 AI 派生内容（话题蒸馏、长楼导览）的产物模型、生成与
// 持久化。
//
// 它与 aireply 的分界线是「产物去哪」：aireply 的产物是一条以机器人身份发布的
// 评论，走与人类完全相同的写路径；这里的产物**不发布**，只落成一行草稿
// （ai_derived_artifacts），等运营者预览之后另行决定它的去向。
//
// 三条边界因此是硬性的：
//   - 本包不写 comments / topics：手工触发的运营动作不该在公共内容里留下痕迹，
//     也不该被审核台当成待审内容（见任务书「偏离登记」D15）；
//   - content 是模型文本，属于用户内容，不得进入 ai_executions（硬约束 1）；
//   - 没有定时任务、没有事件驱动、没有「产物再触发 AI」：触发面只有运营者的
//     手动入口（CLI，将来是控制台按钮）。
package aiderived

import (
	"errors"
	"fmt"
	"strings"
	"time"

	supportai "github.com/keeptrue/sforum/apps/api/app/Support/AI"
)

var (
	// ErrArtifactNotFound 表示请求的产物不存在。
	ErrArtifactNotFound = errors.New("aiderived: artifact not found")
	// ErrArtifactInvalid 表示产物内容或标识不满足存储约束。
	ErrArtifactInvalid = errors.New("aiderived: artifact is invalid")
	// ErrGeneratorUnavailable 表示派生生成缺少装配（没有网关或没有产物存储）。
	ErrGeneratorUnavailable = errors.New("aiderived: generator is not wired")
	// ErrEmptyArtifact 表示模型没有产出正文。它是确定性失败：同样的提示词配
	// 同样的模型只会得到同样的空输出，重试只是把账单乘以尝试次数。
	ErrEmptyArtifact = errors.New("aiderived: model returned an empty artifact")
	// ErrTopicUnavailable 表示话题不存在或对访客不可见。派生只读公开内容，
	// 因此这两种情况在对外语义上是同一个答案（不泄露存在性）。
	ErrTopicUnavailable = errors.New("aiderived: topic is not available")
)

// 派生用途的 id。它们与用途登记表里的那两条声明是同一个字符串：成本等级、
// 工具白名单、提示词版本都由登记表 + 站点覆盖决定，这里不自带一份副本。
const (
	PurposeDigest = supportai.CorePurposeForumDigest
	PurposeTour   = supportai.CorePurposeForumTour
)

// 产物状态。
//
// draft 是本包唯一会写入的状态：手动触发产出的是草稿，发布与否是另一个人的
// 决定。published / discarded 留给将来的发布动作与丢弃动作，先写进约束是为了
// 让「这张表能表达什么」在迁移里就说清楚，而不是等第一次发布时再 ALTER。
const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusDiscarded = "discarded"
)

// 产物体积上限。
//
// 它是给人读的预览文本，不是存档：超过这个长度的「摘要」已经失去摘要的意义，
// 而放宽它只会让运营者在预览面上更容易漏读关键结论。
const (
	// MaxArtifactContentRunes 是单条产物正文的上限。
	MaxArtifactContentRunes = 6_000
	// MaxArtifactTopicID 只是合理性下界校验用的哨兵，避免 0 被当成合法话题。
	MinArtifactTopicID = 1
	// DefaultArtifactListLimit 是一次列取的默认条数。
	DefaultArtifactListLimit = 20
	// MaxArtifactListLimit 是一次列取的硬上限。
	MaxArtifactListLimit = 100
)

// Artifact 是一条 AI 派生内容的草稿。
//
// 字段与迁移 004 的列一一对应，一个都不多：这张表存在的理由是「运营者能预览
// 并处置一份 AI 产物」，而任何额外字段（工具轨迹、token 用量、模型名）都另有
// 归属——轨迹与用量在 ai_executions，模型名在供应商侧，把副本塞进来只会产生
// 第二个真相。
type Artifact struct {
	ID        int64     `json:"id"`
	Purpose   string    `json:"purpose"`
	TopicID   int64     `json:"topicId"`
	RunID     string    `json:"runId,omitempty"`
	PromptVer string    `json:"promptVersion"`
	Status    string    `json:"status"`
	Content   string    `json:"content"`
	CreatedBy int64     `json:"createdByUserId,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

// ValidArtifactPurpose 判定用途是否属于派生内容。
//
// 它是白名单而不是「非空即可」：这张表的 purpose 列会出现在运维查询与将来的
// 控制台列表里，允许任意字符串等于允许把一个拼错的 id 静默写成一条永远匹配不上
// 任何用途声明的孤儿产物。
func ValidArtifactPurpose(purpose string) bool {
	switch strings.TrimSpace(purpose) {
	case PurposeDigest, PurposeTour:
		return true
	default:
		return false
	}
}

// ValidArtifactStatus 限制状态取值。与迁移里的 CHECK 约束保持同一集合。
func ValidArtifactStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case StatusDraft, StatusPublished, StatusDiscarded:
		return true
	default:
		return false
	}
}

// NormalizeArtifact 校验并规整一条待写入的产物。
//
// 它在写入前把「这条产物能否被安全存储」判完，而不是让数据库约束去承担业务
// 校验：约束报错只会得到一句 SQL 错误，而这里能说清是哪一项不合法。
func NormalizeArtifact(artifact Artifact) (Artifact, error) {
	artifact.Purpose = strings.TrimSpace(artifact.Purpose)
	if !ValidArtifactPurpose(artifact.Purpose) {
		return Artifact{}, fmt.Errorf("%w: unsupported purpose %q", ErrArtifactInvalid, artifact.Purpose)
	}
	if artifact.TopicID < MinArtifactTopicID {
		return Artifact{}, fmt.Errorf("%w: topic id must be positive", ErrArtifactInvalid)
	}
	artifact.Status = strings.TrimSpace(artifact.Status)
	if artifact.Status == "" {
		// 缺省即草稿：本包不存在「默认发布」这条路径。
		artifact.Status = StatusDraft
	}
	if !ValidArtifactStatus(artifact.Status) {
		return Artifact{}, fmt.Errorf("%w: unsupported status %q", ErrArtifactInvalid, artifact.Status)
	}
	artifact.PromptVer = strings.TrimSpace(artifact.PromptVer)
	if artifact.PromptVer == "" {
		return Artifact{}, fmt.Errorf("%w: prompt version is required", ErrArtifactInvalid)
	}
	artifact.RunID = strings.TrimSpace(artifact.RunID)
	artifact.Content = strings.TrimSpace(artifact.Content)
	if artifact.Content == "" {
		return Artifact{}, fmt.Errorf("%w: content is empty", ErrArtifactInvalid)
	}
	artifact.Content = ClipRunes(artifact.Content, MaxArtifactContentRunes)
	return artifact, nil
}

// ClipRunes 按 code point 截断文本，超出时追加省略号。
//
// 按 rune 而不是按字节切：按字节切多字节字符会产出非法 UTF-8，存进 JSONB 后
// 整条产物在读取侧就变成乱码——而它正是运营者要审阅的正文。
func ClipRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}

// ClampListLimit 把调用方给的列取条数夹到合理区间。
func ClampListLimit(limit int) int {
	if limit <= 0 {
		return DefaultArtifactListLimit
	}
	if limit > MaxArtifactListLimit {
		return MaxArtifactListLimit
	}
	return limit
}
