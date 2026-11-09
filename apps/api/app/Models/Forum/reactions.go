package forum

import (
	"context"
	"errors"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
)

const (
	// ReactionLike 是 v1 唯一的反应类型。reaction 列已预留多类型，加类型是一次
	// CHECK 迁移 + 一次 UI 变更，不需要改表结构。
	ReactionLike = "like"

	ReactionTargetTopic   = "topic"
	ReactionTargetComment = "comment"

	// CodeReactionsDisabled 是运营者关闭 features.reactions 时的具名原因。
	CodeReactionsDisabled = "forum.reactions_disabled"

	// CodeReactionTargetInvalid 是「目标类型非法/目标无法解析」的具名原因。
	// 它与 topic_not_found / comment_not_found 分开：点赞目标是多态的，
	// 借用主题的 not-found 会在评论目标上给出错误语义。
	CodeReactionTargetInvalid = "forum.reaction_target_invalid"

	// ReactionRosterDefaultLimit / ReactionRosterMaxLimit 约束「谁赞了」名单。
	ReactionRosterDefaultLimit = 20
	ReactionRosterMaxLimit     = 50
	ReactionRosterMaxOffset    = 1000

	// ReactionPreviewLimit 是公开读模型自带的点赞者头像组预览数量：
	// 自己优先、其余最早，最多 3 个。只在读路径装饰，永不进缓存。
	ReactionPreviewLimit = 3
)

var (
	// ErrReactionsDisabled 表示站点产品开关关闭；HTTP 层映射为 403。
	ErrReactionsDisabled = errors.New("forum: reactions are disabled")
	// ErrInvalidReactionTarget 表示目标类型非法或目标不存在/不可见。
	ErrInvalidReactionTarget = errors.New("forum: invalid reaction target")
)

// ReactionState 是一次点赞切换后的权威状态。字段与 OpenAPI ReactionState 同名。
type ReactionState struct {
	TargetType string `json:"targetType"`
	TargetID   int64  `json:"targetId"`
	Reaction   string `json:"reaction"`
	Liked      bool   `json:"liked"`
	LikeCount  int64  `json:"likeCount"`
}

// ReactionRoster 是「谁赞了」的有界名单；Items 复用 UserSummary 的公开形状。
type ReactionRoster struct {
	Items   []UserSummary `json:"items"`
	Total   int64         `json:"total"`
	HasMore bool          `json:"hasMore"`
}

// FeatureGate 读取站点产品开关。未装配时视为开启（与其它产品开关的默认姿态一致）。
type FeatureGate interface {
	IsFeatureEnabled(ctx context.Context, name string) (bool, error)
}

// ReactionReader 为公开读模型补上「当前访客是否已赞」。
//
// 实现必须在缓存之后调用：CachedStore 缓存的是 actor 无关的详情/列表，
// likedByMe 一旦写进缓存就会在访客之间串味。装饰失败必须 fail-open。
type ReactionReader interface {
	DecorateTopic(ctx context.Context, viewer identity.Actor, topic *TopicDetail) error
	DecorateComments(ctx context.Context, viewer identity.Actor, comments []Comment) error
}
