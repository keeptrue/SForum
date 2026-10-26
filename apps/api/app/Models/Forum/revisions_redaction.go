package forum

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	audit "github.com/zhuchunshu/sforum/apps/api/app/Support/Audit"
)

func (s *PostgresStore) RedactTopicRevision(ctx context.Context, input RevisionRedactionRecord) error {
	return s.redactRevision(ctx, input, "topic")
}

func (s *PostgresStore) RedactCommentRevision(ctx context.Context, input RevisionRedactionRecord) error {
	return s.redactRevision(ctx, input, "comment")
}

func (s *PostgresStore) redactRevision(ctx context.Context, input RevisionRedactionRecord, targetType string) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin revision redaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	postID, currentRevision, authorID, err := lockRedactionTarget(ctx, tx, targetType, input.TargetID)
	if err != nil {
		return err
	}
	if currentRevision != input.ExpectedRevision {
		return ErrRevisionConflict
	}
	if input.RevisionNo <= 0 || input.RevisionNo == currentRevision {
		return ErrRevisionRedactionForbidden
	}

	var revisionID int64
	var redacted bool
	err = tx.QueryRow(ctx, `
		SELECT id, redacted_at IS NOT NULL
		FROM post_revisions WHERE post_id = $1 AND revision_no = $2 FOR UPDATE
	`, postID, input.RevisionNo).Scan(&revisionID, &redacted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRevisionNotFound
	}
	if err != nil {
		return fmt.Errorf("lock revision redaction source: %w", err)
	}
	if redacted {
		return ErrRevisionRedacted
	}

	if _, err := tx.Exec(ctx, `
		UPDATE post_revisions
		SET raw_content = '', source_format = '', editor_type = '', editor_version = '',
		    render_version = '', content_hash = '', attachment_ids = ARRAY[]::bigint[],
		    redacted_at = now(), redacted_by_user_id = $2, redaction_reason = $3
		WHERE id = $1
	`, revisionID, input.ActorUserID, input.Reason); err != nil {
		return fmt.Errorf("redact revision payload: %w", err)
	}
	if targetType == "topic" {
		if _, err := tx.Exec(ctx, `UPDATE topic_revision_snapshots SET title = '', category_slug = '', tag_slugs = ARRAY[]::text[] WHERE post_revision_id = $1`, revisionID); err != nil {
			return fmt.Errorf("redact topic revision metadata: %w", err)
		}
	}
	if s.auditor != nil {
		action := audit.ActionForumTopicRevisionRedact
		if targetType == "comment" {
			action = audit.ActionForumCommentRevisionRedact
		}
		if err := s.auditor.AppendTx(ctx, tx, audit.Event{ActorUserID: input.ActorUserID, TargetUserID: authorID, Action: action, Metadata: map[string]any{
			"targetType": targetType, "targetId": input.TargetID, "authorUserId": authorID,
			"revisionId": revisionID, "revisionNo": input.RevisionNo, "operation": "redact",
		}}); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit revision redaction: %w", err)
	}
	return nil
}

// lockRedactionTarget 锁定抹除目标的 content 行。作者注销后 author_user_id 为 NULL
// （ON DELETE SET NULL），这里归一成 0：抹除照常完成，审计 target_user_id 落 NULL
// （audit.Event 约定 0 表示无目标用户），不因为作者已注销而整条请求失败。
func lockRedactionTarget(ctx context.Context, tx pgx.Tx, targetType string, targetID int64) (postID, currentRevision, authorID int64, err error) {
	query := `SELECT content_id, current_revision, COALESCE(topics.author_user_id, 0) FROM topics JOIN posts ON posts.id = topics.content_id WHERE topics.id = $1 FOR UPDATE OF topics, posts`
	notFound := ErrTopicNotFound
	if targetType == "comment" {
		query = `SELECT content_id, current_revision, COALESCE(comments.author_user_id, 0) FROM comments JOIN posts ON posts.id = comments.content_id WHERE comments.id = $1 FOR UPDATE OF comments, posts`
		notFound = ErrCommentNotFound
	}
	err = tx.QueryRow(ctx, query, targetID).Scan(&postID, &currentRevision, &authorID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, notFound
	}
	if err != nil {
		return 0, 0, 0, fmt.Errorf("lock revision redaction target: %w", err)
	}
	return postID, currentRevision, authorID, nil
}
