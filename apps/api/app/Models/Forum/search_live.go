package forum

import (
	"context"
	"fmt"
)

// ListPublicTopicSearchHits 按 id 批量加载仍可公开列表的主题，行形状与 GET /topics 一致：
// author（含 avatar）、lastReplyAuthor、tags、createdAt/lastActivityAt 等。
// 用于搜索引擎命中后的权威 hydrate，避免搜索列表字段残缺或幽灵 id。
func (s *PostgresStore) ListPublicTopicSearchHits(ctx context.Context, ids []int64) (map[int64]TopicSummary, error) {
	out := make(map[int64]TopicSummary, len(ids))
	if s == nil || s.pool == nil || len(ids) == 0 {
		return out, nil
	}
	unique := make([]int64, 0, len(ids))
	seen := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		if id <= 0 {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	if len(unique) == 0 {
		return out, nil
	}

	// 与 list_topics hydrate 列布局一致，复用 scanTopicSummaryWithAvatar。
	rows, err := s.pool.Query(ctx, `
		SELECT topics.id, topics.category_id, categories.slug, categories.name,
		  topics.author_user_id, users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  topics.title, topics.slug, topics.status, topics.is_pinned,
		  topics.comment_count, topics.view_count, topics.like_count, topics.hot_score, `+plainTextPrefixSQL("posts.plain_text")+`,
		  `+effectivePostCurrentRevisionSQL("posts")+`,
		  `+contentEditedSQL("posts")+`,
		  topics.created_at, topics.updated_at, topics.last_activity_at,`+lastReplyAuthorSelectSQL()+`
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		JOIN posts ON posts.id = topics.content_id
		LEFT JOIN users ON users.id = topics.author_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
		`+lastReplyAuthorJoinSQL()+`
		WHERE topics.id = ANY($1::bigint[])
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
	`, unique)
	if err != nil {
		return nil, fmt.Errorf("list public topic search hits: %w", err)
	}
	defer rows.Close()

	items := make([]TopicSummary, 0, len(unique))
	for rows.Next() {
		item, scanErr := scanTopicSummaryWithAvatar(rows, s.avatarBuilder)
		if scanErr != nil {
			return nil, fmt.Errorf("scan public topic search hit: %w", scanErr)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate public topic search hits: %w", err)
	}
	if err := s.attachActiveTagsToTopicSummaries(ctx, items); err != nil {
		return nil, err
	}
	for _, item := range items {
		out[item.ID] = item
	}
	return out, nil
}
