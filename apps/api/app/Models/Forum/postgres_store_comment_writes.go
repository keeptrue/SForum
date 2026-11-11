package forum

// 评论写入与 post 写入辅助（从 postgres_store_ops.go 抽出，逻辑零改动）：
// 这个文件承载 CreateComment 的完整写事务与 post 落库/修订辅助，
// 让 postgres_store_ops.go 回到只做读路径与扫描的职责，并给统计 hook 留出挂载点。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *PostgresStore) CreateComment(ctx context.Context, input CreateCommentRecord) (Comment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin create comment: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	var categoryID int64
	var status string
	if err := tx.QueryRow(ctx, `
		SELECT category_id, status
		FROM topics
		WHERE id = $1
		FOR UPDATE
	`, input.TopicID).Scan(&categoryID, &status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Comment{}, ErrTopicNotFound
		}
		return Comment{}, fmt.Errorf("lock topic for comment: %w", err)
	}
	if status != TopicStatusActive {
		return Comment{}, ErrTopicClosed
	}

	content, err := insertPost(ctx, tx, input.AuthorUserID, input.Content)
	if err != nil {
		return Comment{}, err
	}

	triggerSnapshot, err := json.Marshal(input.ModerationTriggers)
	if err != nil {
		return Comment{}, fmt.Errorf("encode comment moderation triggers: %w", err)
	}
	var commentID int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO comments (topic_id, content_id, author_user_id, parent_comment_id, status, moderation_triggers, ip_address)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, input.TopicID, content.ID, input.AuthorUserID, input.ParentID, input.Status, triggerSnapshot, input.IPAddress).Scan(&commentID); err != nil {
		return Comment{}, fmt.Errorf("insert comment: %w", err)
	}
	position := CommentPositionForInsert(commentID, input.Parent)
	if _, err := tx.Exec(ctx, `
		UPDATE comments
		SET root_comment_id = $2, path_key = $3, depth = $4, updated_at = now()
		WHERE id = $1
	`, commentID, position.RootCommentID, position.PathKey, position.Depth); err != nil {
		return Comment{}, fmt.Errorf("update comment position: %w", err)
	}
	if input.Status == CommentStatusActive && input.ParentID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE comments
			SET reply_count = reply_count + 1, updated_at = now()
			WHERE id = $1
		`, *input.ParentID); err != nil {
			return Comment{}, fmt.Errorf("update parent reply count: %w", err)
		}
	}
	if input.Status == CommentStatusActive {
		// hot_score = comment_count*5 + view_count + like_count*3；+1 评论 → +5。
		if _, err := tx.Exec(ctx, `
			UPDATE topics
			SET comment_count = comment_count + 1,
			    hot_score = `+TopicHotScoreSQL("(comment_count + 1)", "like_count")+`,
			    last_activity_at = now(),
			    updated_at = now()
			WHERE id = $1
		`, input.TopicID); err != nil {
			return Comment{}, fmt.Errorf("update topic comment count: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE categories
			SET comment_count = comment_count + 1, updated_at = now()
			WHERE id = $1
		`, categoryID); err != nil {
			return Comment{}, fmt.Errorf("update category comment count: %w", err)
		}
	}
	if err := replaceForumAttachmentReferences(ctx, tx, "comment", commentID, input.AuthorUserID, input.AttachmentIDs); err != nil {
		return Comment{}, err
	}
	if _, err := insertAcceptedPostRevision(ctx, tx, AcceptedRevisionSnapshotInput{
		PostID:           content.ID,
		RevisionNo:       1,
		ActorUserID:      input.AuthorUserID,
		Operation:        RevisionOperationCreate,
		Origin:           RevisionOriginSelf,
		ChangedFields:    []string{"attachments", "content"},
		AttachmentIDs:    input.AttachmentIDs,
		SnapshotComplete: true,
		Content:          content,
	}); err != nil {
		return Comment{}, err
	}
	if err := setPostCurrentRevision(ctx, tx, content.ID, 1); err != nil {
		return Comment{}, err
	}

	// 计划 D4：comment_count 记「你发过多少条评论」，创建即计数。
	// 软删与审核驳回都不回退——内容行仍存在，只是不可见；贡献已发生。
	if err := bumpUserActivityTx(ctx, tx, input.AuthorUserID, activityMetricCommentCount, 1); err != nil {
		return Comment{}, err
	}

	comment, err := getCommentByID(ctx, tx, commentID, s.avatarBuilder)
	if err != nil {
		return Comment{}, err
	}
	if err := s.notifyCreatedCommentTx(ctx, tx, input, commentID); err != nil {
		return Comment{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit create comment: %w", err)
	}
	// D2：提交后触发一次自动徽章评估（fail-open，失败不影响本次写入）。
	s.enqueueBadgeEvaluation(ctx, input.AuthorUserID)
	return comment, nil
}

func insertPost(ctx context.Context, tx pgx.Tx, userID int64, content RenderedContent) (RenderedContent, error) {
	// excerpt 不落库，由读路径从 plain_text 按运营配置截断派生。
	err := tx.QueryRow(ctx, `
		INSERT INTO posts (
		  raw_content, html_content, plain_text, source_format,
		  editor_type, editor_version, render_version, content_hash,
		  created_by_user_id, updated_by_user_id
		)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $9)
		RETURNING id, created_at, updated_at
	`, content.RawContent, content.HTMLContent, content.PlainText,
		content.SourceFormat, content.EditorType, content.EditorVersion, content.RenderVersion,
		content.ContentHash, userID).Scan(&content.ID, new(time.Time), new(time.Time))
	if err != nil {
		return RenderedContent{}, fmt.Errorf("insert shared post content: %w", err)
	}
	return content, nil
}

func updatePost(ctx context.Context, tx pgx.Tx, postID int64, editorUserID int64, content RenderedContent) error {
	_, err := tx.Exec(ctx, `
		UPDATE posts
		SET raw_content = $2,
		    html_content = $3,
		    plain_text = $4,
		    source_format = $5,
		    editor_type = $6,
		    editor_version = $7,
		    render_version = $8,
		    content_hash = $9,
		    updated_by_user_id = $10,
		    updated_at = now()
		WHERE id = $1
	`, postID, content.RawContent, content.HTMLContent, content.PlainText,
		content.SourceFormat, content.EditorType, content.EditorVersion, content.RenderVersion,
		content.ContentHash, editorUserID)
	if err != nil {
		return fmt.Errorf("update shared post content: %w", err)
	}
	return nil
}

func createPostRevision(ctx context.Context, tx pgx.Tx, postID int64, editorUserID int64) error {
	// revision 只保留源文与渲染元数据，html/plain/excerpt 不重复快照。
	_, err := tx.Exec(ctx, `
		INSERT INTO post_revisions (
		  post_id, superseded_by_user_id, raw_content,
		  source_format, editor_type, editor_version, render_version, content_hash
		)
		SELECT id, $2, raw_content,
		  source_format, editor_type, editor_version, render_version, content_hash
		FROM posts
		WHERE id = $1
	`, postID, editorUserID)
	if err != nil {
		return fmt.Errorf("create post revision: %w", err)
	}
	return nil
}
