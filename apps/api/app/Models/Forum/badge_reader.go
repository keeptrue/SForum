package forum

import "context"

// BadgeRef 是帖子/评论旁展示的「最高级徽章」引用（Forum 侧的读模型）。
//
// 与 badges.BadgeRef 刻意是两份结构：Forum 不能 import Badges（依赖方向），
// 转换由 Badges 侧的 badge_reader.go 完成，bootstrap 注入实现。
//
// actor 无关：同一个作者的徽章对所有人一样，因此它可以随主题/评论进缓存，
// 不像 likedByMe / bookmarkedByMe 必须每请求重算。
type BadgeRef struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	Tier string `json:"tier"`
	Icon string `json:"icon"`
}

// BadgeReader 由 Badges 包实现，用于批量补齐主题/评论作者的最高级徽章。
//
// 实现必须一次查询覆盖整页作者（禁止 N+1），并且失败时由调用方决定是否降级：
// 徽章是装饰信息，拿不到不该让列表整体失败。
type BadgeReader interface {
	DecorateTopics(ctx context.Context, topics []TopicSummary) error
	DecorateComments(ctx context.Context, comments []Comment) error
}
