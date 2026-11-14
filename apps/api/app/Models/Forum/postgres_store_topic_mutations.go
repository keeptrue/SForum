package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateTopic(ctx context.Context, input CreateTopicRecord) (TopicDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TopicDetail{}, fmt.Errorf("begin create topic: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var categoryID int64
	if err := tx.QueryRow(ctx, `
		SELECT id
		FROM categories
		WHERE slug = $1 AND visibility = 'public'
	`, input.CategorySlug).Scan(&categoryID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TopicDetail{}, ErrInvalidTopic
		}
		return TopicDetail{}, fmt.Errorf("load topic category: %w", err)
	}
	content, err := insertPost(ctx, tx, input.AuthorUserID, input.Content)
	if err != nil {
		return TopicDetail{}, err
	}

	triggerSnapshot, err := json.Marshal(input.ModerationTriggers)
	if err != nil {
		return TopicDetail{}, fmt.Errorf("encode topic moderation triggers: %w", err)
	}
	var topicID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO topics (category_id, author_user_id, content_id, title, slug, status, moderation_triggers, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, categoryID, input.AuthorUserID, content.ID, input.Title, input.Slug, input.Status, triggerSnapshot, input.IPAddress).Scan(&topicID); err != nil {
		return TopicDetail{}, fmt.Errorf("insert topic: %w", err)
	}
	if input.Status == TopicStatusActive {
		if _, err := tx.Exec(ctx, `
			UPDATE categories
			SET topic_count = topic_count + 1, updated_at = now()
			WHERE id = $1
		`, categoryID); err != nil {
			return TopicDetail{}, fmt.Errorf("update category topic count: %w", err)
		}
	}
	tags := input.Tags
	if len(tags) == 0 && len(input.TagSlugs) > 0 {
		tags, err = resolveTopicTags(ctx, tx, ResolveTopicTagsInput{
			ActorUserID:  input.AuthorUserID,
			Slugs:        input.TagSlugs,
			CreationMode: input.TagCreationMode,
		})
		if err != nil {
			return TopicDetail{}, err
		}
	}
	if err := attachTopicTags(ctx, tx, topicID, tags); err != nil {
		return TopicDetail{}, err
	}
	if err := replaceForumAttachmentReferences(ctx, tx, "topic", topicID, input.AuthorUserID, input.AttachmentIDs); err != nil {
		return TopicDetail{}, err
	}
	if _, err := insertAcceptedPostRevision(ctx, tx, AcceptedRevisionSnapshotInput{
		PostID:           content.ID,
		RevisionNo:       1,
		ActorUserID:      input.AuthorUserID,
		Operation:        RevisionOperationCreate,
		Origin:           RevisionOriginSelf,
		ChangedFields:    []string{"attachments", "category", "content", "tags", "title"},
		AttachmentIDs:    input.AttachmentIDs,
		SnapshotComplete: true,
		Content:          content,
		Topic: &TopicRevisionSnapshotInput{
			TopicID:      topicID,
			Title:        input.Title,
			CategorySlug: input.CategorySlug,
			TagSlugs:     topicTagSlugs(tags),
		},
	}); err != nil {
		return TopicDetail{}, err
	}
	if err := setPostCurrentRevision(ctx, tx, content.ID, 1); err != nil {
		return TopicDetail{}, err
	}
	row := tx.QueryRow(ctx, topicDetailSQL()+` WHERE topics.id = $1`, topicID)
	topic, err := scanTopicDetailWithAvatar(row, s.avatarBuilder)
	if err != nil {
		return TopicDetail{}, fmt.Errorf("read created topic: %w", err)
	}
	topic.Tags = tags
	if s.notifications != nil {
		if input.Status == TopicStatusActive {
			if err := s.notifications.NotifyTopicTx(ctx, tx, TopicNotificationInput{TopicID: topicID, ActorUserID: input.AuthorUserID, MentionedUsernames: input.MentionedUsernames}); err != nil {
				return TopicDetail{}, fmt.Errorf("create topic notifications: %w", err)
			}
		} else if input.Status == TopicStatusPending {
			if err := s.notifications.NotifyPendingReviewTx(ctx, tx, PendingReviewNotificationInput{TargetType: "topic", TargetID: topicID, TopicID: topicID, AuthorUserID: input.AuthorUserID, Revision: 1, Title: input.Title}); err != nil {
				return TopicDetail{}, fmt.Errorf("create pending topic notifications: %w", err)
			}
		}
	}
	// 计划 D4：topic_count 记「你发过多少个主题」，创建即计数；软删不回退。
	if err := bumpUserActivityTx(ctx, tx, input.AuthorUserID, activityMetricTopicCount, 1); err != nil {
		return TopicDetail{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return TopicDetail{}, fmt.Errorf("commit create topic: %w", err)
	}
	// D2：提交后触发一次自动徽章评估（fail-open）。
	s.enqueueUserEvaluations(ctx, input.AuthorUserID)
	return topic, nil
}

// updateTopicLegacy is retained temporarily as a migration reference. M3 uses
// updateTopicVersioned so ordinary edits never create superseded snapshots.
func (s *PostgresStore) updateTopicLegacy(ctx context.Context, input UpdateTopicRecord) (TopicDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TopicDetail{}, fmt.Errorf("begin update topic: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	// 锁定主题行，确认存在且未删除。
	var categoryID int64
	var contentID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT category_id, content_id, status
		FROM topics
		WHERE id = $1 AND status <> 'deleted'
		FOR UPDATE
	`, input.TopicID).Scan(&categoryID, &contentID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TopicDetail{}, ErrTopicNotFound
		}
		return TopicDetail{}, fmt.Errorf("lock topic for update: %w", err)
	}

	// 更新分类。
	if input.CategorySlug != "" {
		var newCategoryID int64
		if err := tx.QueryRow(ctx, `
			SELECT id FROM categories WHERE slug = $1 AND visibility = 'public'
		`, input.CategorySlug).Scan(&newCategoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return TopicDetail{}, ErrInvalidTopic
			}
			return TopicDetail{}, fmt.Errorf("load update topic category: %w", err)
		}
		// 仅 active 主题计入 category.topic_count；pending/hidden 等移动不改计数。
		if newCategoryID != categoryID {
			if status == TopicStatusActive {
				if _, err := tx.Exec(ctx, `
					UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now() WHERE id = $1
				`, categoryID); err != nil {
					return TopicDetail{}, fmt.Errorf("decrement old category count: %w", err)
				}
				if _, err := tx.Exec(ctx, `
					UPDATE categories SET topic_count = topic_count + 1, updated_at = now() WHERE id = $1
				`, newCategoryID); err != nil {
					return TopicDetail{}, fmt.Errorf("increment new category count: %w", err)
				}
			}
			categoryID = newCategoryID
		}
	}

	// 更新正文：先存历史版本再覆盖 posts 记录。
	if input.HasContent {
		if err := createPostRevision(ctx, tx, contentID, input.EditorUserID); err != nil {
			return TopicDetail{}, err
		}
		if err := updatePost(ctx, tx, contentID, input.EditorUserID, input.Content); err != nil {
			return TopicDetail{}, err
		}
	}

	// 编辑触发预审：active 主题降为 pending，并回滚分类计数。
	if input.RequeuePending {
		triggerSnapshot, err := json.Marshal(input.ModerationTriggers)
		if err != nil {
			return TopicDetail{}, fmt.Errorf("encode topic moderation triggers: %w", err)
		}
		wasActive := status == TopicStatusActive
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET status = 'pending', moderation_triggers = $2, updated_at = now()
			WHERE id = $1
		`, input.TopicID, triggerSnapshot); err != nil {
			return TopicDetail{}, fmt.Errorf("requeue topic pending: %w", err)
		}
		status = TopicStatusPending
		if wasActive {
			if _, err := tx.Exec(ctx, `
				UPDATE categories
				SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now()
				WHERE id = $1
			`, categoryID); err != nil {
				return TopicDetail{}, fmt.Errorf("decrement category after requeue: %w", err)
			}
		}
	}

	// 更新主题标题/slug（标题变更时同步 slug）。
	if input.Title != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET title = $2, slug = $3, updated_at = now(), last_activity_at = now()
			WHERE id = $1
		`, input.TopicID, input.Title, input.Slug); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic title: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE topics SET updated_at = now() WHERE id = $1
	`, input.TopicID); err != nil {
		return TopicDetail{}, fmt.Errorf("touch topic: %w", err)
	}

	// 记录最近一次编辑 IP（创建 ip_address 保持不变；空串表示调用方未注入）。
	if input.LastEditIP != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE topics SET last_edit_ip = $2 WHERE id = $1
		`, input.TopicID, input.LastEditIP); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic last_edit_ip: %w", err)
		}
	}

	// 更新分类外键（若分类变更）。
	if input.CategorySlug != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE topics SET category_id = $2 WHERE id = $1
		`, input.TopicID, categoryID); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic category: %w", err)
		}
	}

	// 更新标签（若传入 tagSlugs，全量替换）。
	if input.TagSlugs != nil {
		if err := replaceTopicTags(ctx, tx, input.TopicID, input.TagSlugs, input.TagCreationMode, input.EditorUserID); err != nil {
			return TopicDetail{}, err
		}
	}
	if input.ReplaceAttachments {
		if err := replaceForumAttachmentReferences(ctx, tx, "topic", input.TopicID, input.EditorUserID, input.AttachmentIDs); err != nil {
			return TopicDetail{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return TopicDetail{}, fmt.Errorf("commit update topic: %w", err)
	}
	return s.GetTopic(ctx, input.TopicID)
}

// replaceTopicTags 全量替换主题标签：删除旧关联、解绑旧标签计数、重新解析并附加新标签。
func replaceTopicTags(ctx context.Context, tx pgx.Tx, topicID int64, slugs []string, creationMode string, actorUserID int64) error {
	// 减去旧标签计数（仅 active 标签）。
	if _, err := tx.Exec(ctx, `
		UPDATE tags
		SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now()
		FROM topic_tags
		WHERE topic_tags.topic_id = $1
		  AND topic_tags.tag_id = tags.id
		  AND tags.status = 'active'
	`, topicID); err != nil {
		return fmt.Errorf("decrement old tag counts: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM topic_tags WHERE topic_id = $1
	`, topicID); err != nil {
		return fmt.Errorf("clear topic tags: %w", err)
	}
	tags, err := resolveTopicTags(ctx, tx, ResolveTopicTagsInput{
		ActorUserID:  actorUserID,
		Slugs:        slugs,
		CreationMode: creationMode,
	})
	if err != nil {
		return err
	}
	return attachTopicTags(ctx, tx, topicID, tags)
}

func clearTopicAttachmentReferences(ctx context.Context, tx pgx.Tx, topicID int64) error {
	if _, err := tx.Exec(ctx, `
		WITH removed AS (
			DELETE FROM attachment_references
			WHERE context = $2 AND (
			  (resource_type = 'topic' AND resource_id = $1) OR
			  (resource_type = 'comment' AND resource_id IN (SELECT id FROM comments WHERE topic_id = $1))
			)
			RETURNING attachment_id
		), counts AS (
			SELECT attachment_id, COUNT(*)::integer AS amount FROM removed GROUP BY attachment_id
		)
		UPDATE attachments a
		SET reference_count = GREATEST(a.reference_count - counts.amount, 0), updated_at = now()
		FROM counts
		WHERE a.id = counts.attachment_id
	`, topicID, forumAttachmentContext); err != nil {
		return fmt.Errorf("clear topic attachment references: %w", err)
	}
	return nil
}

func (s *PostgresStore) DeleteTopic(ctx context.Context, topicID int64) (TopicDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TopicDetail{}, fmt.Errorf("begin delete topic: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var categoryID int64
	var prevStatus string
	if err := tx.QueryRow(ctx, `
		SELECT category_id, status
		FROM topics
		WHERE id = $1 AND status <> 'deleted'
		FOR UPDATE
	`, topicID).Scan(&categoryID, &prevStatus); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TopicDetail{}, ErrTopicNotFound
		}
		return TopicDetail{}, fmt.Errorf("lock topic for delete: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE topics
		SET status = 'deleted', deleted_at = COALESCE(deleted_at, now()), updated_at = now()
		WHERE id = $1
	`, topicID); err != nil {
		return TopicDetail{}, fmt.Errorf("soft delete topic: %w", err)
	}
	// 仅曾计入公开计数的 active 主题才回滚 category.topic_count。
	if prevStatus == TopicStatusActive {
		if _, err := tx.Exec(ctx, `
			UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now() WHERE id = $1
		`, categoryID); err != nil {
			return TopicDetail{}, fmt.Errorf("decrement category count on delete: %w", err)
		}
	}
	if err := clearTopicAttachmentReferences(ctx, tx, topicID); err != nil {
		return TopicDetail{}, err
	}

	// 读取删除后的主题快照（不做公开可见性过滤）。
	row := tx.QueryRow(ctx, topicDetailSQL()+`
		WHERE topics.id = $1
	`, topicID)
	topic, err := scanTopicDetailWithAvatar(row, s.avatarBuilder)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TopicDetail{}, ErrTopicNotFound
		}
		return TopicDetail{}, fmt.Errorf("get deleted topic: %w", err)
	}
	tags, err := s.activeTopicTags(ctx, []int64{topic.ID})
	if err != nil {
		return TopicDetail{}, err
	}
	topic.Tags = tags[topic.ID]

	if err := tx.Commit(ctx); err != nil {
		return TopicDetail{}, fmt.Errorf("commit delete topic: %w", err)
	}
	return topic, nil
}
