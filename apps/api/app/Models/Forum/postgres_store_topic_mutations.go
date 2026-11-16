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
	var contentID int64
	if err := tx.QueryRow(ctx, `
		SELECT category_id, status, content_id
		FROM topics
		WHERE id = $1 AND status <> 'deleted'
		FOR UPDATE
	`, topicID).Scan(&categoryID, &prevStatus, &contentID); err != nil {
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
	if err := bumpContentRevisionOnDeleteTx(ctx, tx, contentID); err != nil {
		return TopicDetail{}, err
	}
	// 仅曾计入公开计数的主题才回滚 category.topic_count。公开口径是 active+locked：
	// 只判 active 会让删除 locked 主题后的计数永久虚高（P2-5）。
	if TopicCountsPublic(prevStatus) {
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
