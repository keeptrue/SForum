package forum

import "context"

// AuthorTrustLevel 是作者信任等级的**读模型**（level / levelSlug / color）。
//
// 与 TrustLevels 包的 AuthorLevel 刻意是两份结构：Forum 不 import TrustLevels
// （依赖方向 Identity → TrustLevels），转换由装配层的适配器完成。
//
// 只含展示三项：不带 progress、不带 bottleneck、不带任何行为计数——
// like_given_count / bookmark_count 是刻意不公开的窥探面（D1），等级徽章只需要
// 「第几档 + 身份 + 颜色」。
type AuthorTrustLevel struct {
	Level     int    `json:"level"`
	LevelSlug string `json:"levelSlug"`
	Color     string `json:"color"`
}

// AuthorTrustLevelSource 由装配层实现：按作者 id 批量折算等级。
//
// 实现必须一次调用覆盖整页作者（禁止 N+1）。失败时由调用方 fail-open 成
// 「不带等级」：徽章是增强面，拿不到不该让列表/详情 500。
type AuthorTrustLevelSource interface {
	AuthorTrustLevels(ctx context.Context, userIDs []int64) (map[int64]AuthorTrustLevel, error)
}

// AuthorTrustLevelReader 是读面的聚焦协作者：持有来源并把整页作者折成等级。
//
// 刻意不是 Service 上的方法：Service 已经有 76 个 receiver（架构基线封顶），
// 把「装饰读面」挂上去只会继续制造 God object。读面自己持有这个小对象即可。
type AuthorTrustLevelReader struct {
	source AuthorTrustLevelSource
}

// NewAuthorTrustLevelReader 构造批量读取器；source 为 nil 时关闭该能力。
func NewAuthorTrustLevelReader(source AuthorTrustLevelSource) *AuthorTrustLevelReader {
	return &AuthorTrustLevelReader{source: source}
}

// TrustLevelBatch 是包级批量入口：一次调用覆盖整页作者。
//
// 未装配来源时返回 (nil, nil)：装饰是可选能力，未注入等于关闭，不是错误。
// id 先去重再交给实现，所以「同一作者出现 20 次」不会变成 20 次查询。
func (r *AuthorTrustLevelReader) TrustLevelBatch(ctx context.Context, userIDs []int64) (map[int64]AuthorTrustLevel, error) {
	if r == nil || r.source == nil {
		return nil, nil
	}
	ids := uniqueAuthorIDs(userIDs)
	if len(ids) == 0 {
		return map[int64]AuthorTrustLevel{}, nil
	}
	return r.source.AuthorTrustLevels(ctx, ids)
}

// applyTopicAuthorTrustLevels 把批量结果贴到主题作者上（缺失的作者保持无字段）。
func applyTopicAuthorTrustLevels(topics []TopicSummary, levels map[int64]AuthorTrustLevel) {
	for i := range topics {
		if topics[i].Author == nil {
			continue
		}
		if level, ok := levels[topics[i].Author.ID]; ok {
			applied := level
			topics[i].AuthorTrustLevel = &applied
		}
	}
}

// applyCommentAuthorTrustLevels 把批量结果贴到评论作者上（含 tree 视图的嵌套子树）。
//
// 递归是必须的：tree 视图把子孙评论内联在 `children` 里，只贴顶层会让「楼中楼」的
// 作者全部丢徽章，而它们正是最需要等级线索的那批行。
func applyCommentAuthorTrustLevels(comments []Comment, levels map[int64]AuthorTrustLevel) {
	for i := range comments {
		if comments[i].Author != nil {
			if level, ok := levels[comments[i].Author.ID]; ok {
				applied := level
				comments[i].AuthorTrustLevel = &applied
			}
		}
		applyCommentAuthorTrustLevels(comments[i].Children, levels)
	}
}

// topicAuthorIDs 收集一页主题的作者 id（只算主题作者，不含最近回复作者）。
func topicAuthorIDs(topics []TopicSummary) []int64 {
	ids := make([]int64, 0, len(topics))
	for i := range topics {
		if topics[i].Author != nil && topics[i].Author.ID > 0 {
			ids = append(ids, topics[i].Author.ID)
		}
	}
	return ids
}

// commentAuthorIDs 收集一页评论（含 tree 嵌套子树）的作者 id。
func commentAuthorIDs(comments []Comment) []int64 {
	ids := make([]int64, 0, len(comments))
	collectCommentAuthorIDs(comments, &ids)
	return ids
}

func collectCommentAuthorIDs(comments []Comment, ids *[]int64) {
	for i := range comments {
		if comments[i].Author != nil && comments[i].Author.ID > 0 {
			*ids = append(*ids, comments[i].Author.ID)
		}
		collectCommentAuthorIDs(comments[i].Children, ids)
	}
}

// uniqueAuthorIDs 去重并剔除非正 id，保持输入顺序。
func uniqueAuthorIDs(userIDs []int64) []int64 {
	seen := make(map[int64]bool, len(userIDs))
	out := make([]int64, 0, len(userIDs))
	for _, id := range userIDs {
		if id <= 0 || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
