package forum

import "strings"

// topicSummaryColumns 是主题摘要的唯一列清单。
//
// 为什么必须只有一份：scanTopicSummaryWithAvatar 是位置扫描，谁手写第二份列清单，
// 谁就会在下次加列时静默错位或直接 500（"number of field descriptions must equal
// number of destinations"）。目前有三个 SELECT 复用它：
//   - topicSummarySQL()（单行读取，FROM topics）
//   - list_topics.go 的首页分页（FROM page CTE）
//   - search_live.go 的搜索结果（FROM topics + ANY($1)）
//
// 三者的 FROM/JOIN 确实不同，但**列清单必须相同**。
//
// 任何新增列都要同时改 scanTopicSummaryWithAvatar 的扫描目的地，并同步
// badge_columns_test.go 的列尾逐字比对（钉顺序）；「SQL 列数 = 目的地数」由
// badge_tier_appearance_postgres_integration_test.go 的真库扫描覆盖——那里列数
// 对不上会直接报 scan 错误，而不是静默读脏数据。
func topicSummaryColumns() string {
	return `
		topics.id, topics.category_id, categories.slug, categories.name,
		  topics.author_user_id, users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  topics.title, topics.slug, topics.status, topics.is_pinned,
		  topics.comment_count, topics.view_count, topics.like_count, topics.hot_score, ` + plainTextPrefixSQL("posts.plain_text") + `,
		  ` + effectivePostCurrentRevisionSQL("posts") + `,
		  ` + contentEditedSQL("posts") + `,
		  topics.created_at, topics.updated_at, topics.last_activity_at,` + lastReplyAuthorSelectSQL() + `,
		  ` + topBadgeSelectSQL()
}

func topicSummarySQL() string {
	// plain_text 前缀供读路径派生 excerpt（列已删除，避免列表拉全量正文）。
	// hot_score 供 M5 keyset 编码（json:"-"，不暴露公开 JSON）。
	return `
		SELECT ` + topicSummaryColumns() + `
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		JOIN posts ON posts.id = topics.content_id
		LEFT JOIN users ON users.id = topics.author_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
		` + lastReplyAuthorJoinSQL() + topBadgeJoinSQL()
}

// topicListOrderBy：置顶始终优先，再按运营默认排序。
// latest=创建时间；active=最后活跃（可走 topics_category_activity_idx / topics_public_activity_idx）；
// hot=topics.hot_score（M2 列 + topics_*_hot_idx）。
func topicListOrderBy(sort string) string {
	switch strings.TrimSpace(strings.ToLower(sort)) {
	case "latest":
		return `ORDER BY topics.is_pinned DESC, topics.created_at DESC, topics.id DESC`
	case "hot":
		// 与 topics_public_hot_idx / topics_category_hot_idx 对齐（is_pinned, hot_score, id）。
		return `ORDER BY topics.is_pinned DESC, topics.hot_score DESC, topics.id DESC`
	default: // active — 与 topics_category_activity_idx (category_id, is_pinned DESC, last_activity_at DESC, id DESC) 对齐
		return `ORDER BY topics.is_pinned DESC, topics.last_activity_at DESC, topics.id DESC`
	}
}
