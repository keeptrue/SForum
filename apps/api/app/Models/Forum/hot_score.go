package forum

import "fmt"

// 热度公式的唯一权威：comment_count*5 + view_count + like_count*3。
//
// 为什么必须有这一个入口：hot_score 是反规范化列，任何一次重算都必须写出完整
// 公式。历史上有六处写点各自抄了一遍 `comment_count*5 + view_count`，新增点赞
// 信号时只要漏掉一处，下一次「审核通过」「删除评论」就会把点赞贡献静默抹掉。
// 所有重算路径（评论增删、审核动作、点赞增删）都必须调用这里。
//
// commentExpr / likeExpr 表达「即将写入的新值」，例如 "(comment_count + 1)" 或
// "GREATEST(comment_count - 1, 0)"。view_count 不在写点里做增量，直接引用列值。
func TopicHotScoreSQL(commentExpr, likeExpr string) string {
	return fmt.Sprintf("%s * 5 + view_count + %s * 3", commentExpr, likeExpr)
}
