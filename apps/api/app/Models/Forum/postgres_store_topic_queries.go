package forum

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) ListAllTopicIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id FROM topics
		WHERE status IN ('active', 'locked')
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("list all topic ids: %w", err)
	}
	defer rows.Close()

	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan topic id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate topic ids: %w", err)
	}
	return ids, nil
}

func (s *PostgresStore) GetTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	row := s.pool.QueryRow(ctx, topicDetailSQL()+`
		WHERE topics.id = $1
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
	`, topicID)
	topic, err := scanTopicDetailWithAvatar(row, s.avatarBuilder)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicDetail{}, ErrTopicNotFound
	}
	if err != nil {
		return TopicDetail{}, fmt.Errorf("get topic: %w", err)
	}
	tags, err := s.activeTopicTags(ctx, []int64{topic.ID})
	if err != nil {
		return TopicDetail{}, err
	}
	topic.Tags = tags[topic.ID]
	if err := s.attachTopicContributors(ctx, &topic); err != nil {
		return TopicDetail{}, err
	}
	return topic, nil
}

func (s *PostgresStore) ListAuthorReviewItems(ctx context.Context, authorUserID int64) (AuthorReviewList, error) {
	// excerpt 列已删除：取 plain_text 前缀，扫描后按默认 rune 上限派生。
	plainPrefix := plainTextPrefixSQL("posts.plain_text")
	rows, err := s.pool.Query(ctx, `
		WITH author_items AS (
		  SELECT 'topic'::text AS target_type, topics.id AS target_id, topics.id AS topic_id,
		    topics.title, `+plainPrefix+` AS plain_prefix, topics.status, topics.created_at
		  FROM topics JOIN posts ON posts.id = topics.content_id
		  WHERE topics.author_user_id = $1 AND topics.status IN ('pending', 'rejected')
		  UNION ALL
		  SELECT 'comment'::text, comments.id, comments.topic_id, topics.title,
		    `+plainPrefix+`, comments.status, comments.created_at
		  FROM comments
		  JOIN posts ON posts.id = comments.content_id
		  JOIN topics ON topics.id = comments.topic_id
		  WHERE comments.author_user_id = $1 AND comments.status IN ('pending', 'rejected')
		)
		SELECT items.target_type, items.target_id, items.topic_id, items.title, items.plain_prefix,
		  items.status, COALESCE(decision.review_note, ''), items.created_at
		FROM author_items items
		LEFT JOIN LATERAL (
		  SELECT review_note FROM moderation_decisions
		  WHERE source = 'pre_publish' AND target_type = items.target_type AND target_id = items.target_id
		  ORDER BY created_at DESC, id DESC LIMIT 1
		) decision ON TRUE
		ORDER BY items.created_at DESC, items.target_id DESC
	`, authorUserID)
	if err != nil {
		return AuthorReviewList{}, fmt.Errorf("list author review items: %w", err)
	}
	defer rows.Close()
	items := make([]AuthorReviewItem, 0)
	for rows.Next() {
		var item AuthorReviewItem
		var plainPrefix string
		if err := rows.Scan(&item.TargetType, &item.TargetID, &item.TopicID, &item.Title,
			&plainPrefix, &item.Status, &item.ReviewNote, &item.CreatedAt); err != nil {
			return AuthorReviewList{}, fmt.Errorf("scan author review item: %w", err)
		}
		item.Excerpt = ExcerptFromPlain(plainPrefix, defaultExcerptRuneLimit)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AuthorReviewList{}, fmt.Errorf("iterate author review items: %w", err)
	}
	return AuthorReviewList{Items: items}, nil
}

// GetTopicBySlug 按全局唯一 slug 查询公开主题。
// WHERE topics.slug = $1 走 UNIQUE 索引 topics_slug_idx（迁移 202607090001）。
// 与 GetTopic 共用 topicDetailSQL()（posts + author avatar；revision token/edited 由热列或混合期页内计数派生）。
// slug 为空或无匹配时返回 ErrTopicNotFound。
func (s *PostgresStore) GetTopicBySlug(ctx context.Context, slug string) (TopicDetail, error) {
	if strings.TrimSpace(slug) == "" {
		return TopicDetail{}, ErrTopicNotFound
	}
	// 单行点查：slug 唯一索引 + posts_pkey + 可选 avatar；tags 二次查询（轻量）。
	row := s.pool.QueryRow(ctx, topicDetailSQL()+`
		WHERE topics.slug = $1
		  AND topics.status IN ('active', 'locked')
		  AND categories.visibility = 'public'
	`, slug)
	topic, err := scanTopicDetailWithAvatar(row, s.avatarBuilder)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicDetail{}, ErrTopicNotFound
	}
	if err != nil {
		return TopicDetail{}, fmt.Errorf("get topic by slug: %w", err)
	}
	tags, err := s.activeTopicTags(ctx, []int64{topic.ID})
	if err != nil {
		return TopicDetail{}, err
	}
	topic.Tags = tags[topic.ID]
	if err := s.attachTopicContributors(ctx, &topic); err != nil {
		return TopicDetail{}, err
	}
	return topic, nil
}

// TopicSlugExists 检查 slug 是否已被占用。统计所有状态（含 hidden/deleted）的主题，
// 避免删除/隐藏主题后其 slug 被新主题复用造成 URL 歧义。excludeTopicID 用于更新时排除自身。
func (s *PostgresStore) TopicSlugExists(ctx context.Context, slug string, excludeTopicID int64) (bool, error) {
	if strings.TrimSpace(slug) == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM topics WHERE slug = $1 AND ($2 = 0 OR id <> $2))
	`, slug, excludeTopicID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("topic slug exists: %w", err)
	}
	return exists, nil
}

func (s *PostgresStore) ActiveTopicTitleExists(ctx context.Context, title string, excludeTopicID int64) (bool, error) {
	title = strings.TrimSpace(title)
	if title == "" {
		return false, nil
	}
	var exists bool
	// 仅 active/locked 参与重复判定；pending/hidden/deleted 不挡新帖。
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM topics
			WHERE lower(title) = lower($1)
			  AND status IN ('active', 'locked')
			  AND ($2 = 0 OR id <> $2)
		)
	`, title, excludeTopicID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("topic title exists: %w", err)
	}
	return exists, nil
}

// AutoLockIdleTopics 批量锁定闲置 active 主题；返回实际更新行数。
func (s *PostgresStore) AutoLockIdleTopics(ctx context.Context, idleDays int, limit int) (int, error) {
	if idleDays <= 0 {
		return 0, nil
	}
	if limit <= 0 {
		limit = 100
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE topics
		SET status = 'locked',
		    locked_at = COALESCE(locked_at, now()),
		    updated_at = now()
		WHERE id IN (
			SELECT id FROM topics
			WHERE status = 'active'
			  AND last_activity_at < now() - ($1::int * interval '1 day')
			ORDER BY last_activity_at ASC
			LIMIT $2
		)
	`, idleDays, limit)
	if err != nil {
		return 0, fmt.Errorf("auto lock idle topics: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// ApplyViewCountDeltas 将 Redis 刷盘增量写入 view_count，并同步 hot_score（+delta）。
// 禁止在公开 GET 详情路径调用；仅供 forum.flush_view_counts job。
func (s *PostgresStore) ApplyViewCountDeltas(ctx context.Context, deltas map[int64]int64) (int, error) {
	if len(deltas) == 0 {
		return 0, nil
	}
	updated := 0
	for topicID, delta := range deltas {
		if topicID <= 0 || delta <= 0 {
			continue
		}
		// 只动计数：updated_at 是「内容更新时间」的公开语义，浏览不是内容变更。
		// 在这里写 now() 会让「看帖」把主题顶成刚更新（RV-52）。
		tag, err := s.pool.Exec(ctx, `
			UPDATE topics
			SET view_count = view_count + $2,
			    hot_score = hot_score + $2
			WHERE id = $1
		`, topicID, delta)
		if err != nil {
			return updated, fmt.Errorf("apply view delta topic %d: %w", topicID, err)
		}
		if tag.RowsAffected() > 0 {
			updated++
		}
	}
	return updated, nil
}

func (s *PostgresStore) attachActiveTagsToTopicSummaries(ctx context.Context, items []TopicSummary) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]int64, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	tags, err := s.activeTopicTags(ctx, ids)
	if err != nil {
		return err
	}
	for index := range items {
		items[index].Tags = tags[items[index].ID]
	}
	return nil
}

func (s *PostgresStore) activeTopicTags(ctx context.Context, topicIDs []int64) (map[int64][]TopicTagSummary, error) {
	result := map[int64][]TopicTagSummary{}
	if len(topicIDs) == 0 {
		return result, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT topic_tags.topic_id, tags.id, tags.slug, tags.name, tags.status
		FROM topic_tags
		JOIN tags ON tags.id = topic_tags.tag_id
		WHERE topic_tags.topic_id = ANY($1)
		  AND tags.status = 'active'
		ORDER BY topic_tags.topic_id ASC, tags.name ASC, tags.id ASC
	`, topicIDs)
	if err != nil {
		return nil, fmt.Errorf("list active topic tags: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var topicID int64
		var tag TopicTagSummary
		if err := rows.Scan(&topicID, &tag.ID, &tag.Slug, &tag.Name, &tag.Status); err != nil {
			return nil, fmt.Errorf("scan active topic tag: %w", err)
		}
		result[topicID] = append(result[topicID], tag)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active topic tags: %w", err)
	}
	return result, nil
}

func (s *PostgresStore) ResolveTopicTags(ctx context.Context, input ResolveTopicTagsInput) ([]TopicTagSummary, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, fmt.Errorf("begin resolve topic tags: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	tags, err := resolveTopicTags(ctx, tx, input)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit resolve topic tags: %w", err)
	}
	return tags, nil
}
