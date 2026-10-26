package forum

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// UpdateTopic is the M3 canonical edit transaction. Its locked CAS check is
// intentionally repeated after the service's pre-filter check: filters may
// block for long enough for another accepted edit to commit.
func (s *PostgresStore) UpdateTopic(ctx context.Context, input UpdateTopicRecord) (TopicDetail, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return TopicDetail{}, fmt.Errorf("begin versioned topic update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	state, err := lockTopicEditState(ctx, tx, input.TopicID)
	if err != nil {
		return TopicDetail{}, err
	}
	if state.currentRevision != input.ExpectedRevision {
		return TopicDetail{}, ErrRevisionConflict
	}
	if err := validateRestoreSourceTx(ctx, tx, state.postID, input.RestoredFromRevisionID); err != nil {
		return TopicDetail{}, err
	}
	state.tags, err = topicTagSlugsTx(ctx, tx, input.TopicID)
	if err != nil {
		return TopicDetail{}, err
	}
	state.attachments, err = attachmentIDsTx(ctx, tx, "topic", input.TopicID)
	if err != nil {
		return TopicDetail{}, err
	}

	final := state
	if input.Title != "" {
		final.title = input.Title
	}
	if input.CategorySlug != "" {
		final.categorySlug = input.CategorySlug
	}
	if input.TagSlugs != nil {
		final.tags = normalizeRevisionTextArray(input.TagSlugs)
	}
	if input.HasContent {
		final.content = input.Content
	}
	if input.ReplaceAttachments {
		final.attachments = normalizeRevisionInt64Array(input.AttachmentIDs)
	}
	if isRestoreOperation(input.Operation) && input.ReplaceAttachments {
		if err := validateRestoreAttachmentSnapshotTx(ctx, tx, input.RestoredFromRevisionID, final.attachments); err != nil {
			return TopicDetail{}, err
		}
		if err := validateHistoricalAttachmentRebindTx(ctx, tx, final.attachments, input.HistoricalAttachmentOwnerID); err != nil {
			return TopicDetail{}, err
		}
	}
	if isRestoreOperation(input.Operation) {
		if err := validateHistoricalTopicTagsTx(ctx, tx, final.tags, input.TagCreationMode); err != nil {
			return TopicDetail{}, err
		}
	}
	changed := changedTopicSnapshotFields(state, final)
	if len(changed) == 0 && !isRestoreOperation(input.Operation) {
		topic, err := readTopicForWriteTx(ctx, tx, s, input.TopicID)
		if err != nil {
			return TopicDetail{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return TopicDetail{}, fmt.Errorf("commit topic no-op: %w", err)
		}
		return topic, nil
	}

	categoryID := state.categoryID
	if input.CategorySlug != "" {
		if err := tx.QueryRow(ctx, `SELECT id FROM categories WHERE slug = $1 AND visibility = 'public'`, final.categorySlug).Scan(&categoryID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if isRestoreOperation(input.Operation) {
					return TopicDetail{}, ErrRevisionCategoryUnavailable
				}
				return TopicDetail{}, ErrInvalidTopic
			}
			return TopicDetail{}, fmt.Errorf("load topic edit category: %w", err)
		}
		if categoryID != state.categoryID && state.status == TopicStatusActive {
			if _, err := tx.Exec(ctx, `UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now() WHERE id = $1`, state.categoryID); err != nil {
				return TopicDetail{}, fmt.Errorf("decrement old topic category: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE categories SET topic_count = topic_count + 1, updated_at = now() WHERE id = $1`, categoryID); err != nil {
				return TopicDetail{}, fmt.Errorf("increment new topic category: %w", err)
			}
		}
	}
	if input.HasContent && !sameRevisionContent(state.content, final.content) {
		if err := updatePost(ctx, tx, state.postID, input.EditorUserID, final.content); err != nil {
			return TopicDetail{}, err
		}
	}
	if input.Title != "" {
		if _, err := tx.Exec(ctx, `UPDATE topics SET title = $2, slug = $3, updated_at = now(), last_activity_at = now() WHERE id = $1`, input.TopicID, final.title, input.Slug); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic title: %w", err)
		}
	} else if _, err := tx.Exec(ctx, `UPDATE topics SET updated_at = now() WHERE id = $1`, input.TopicID); err != nil {
		return TopicDetail{}, fmt.Errorf("touch topic: %w", err)
	}
	if input.LastEditIP != "" {
		if _, err := tx.Exec(ctx, `UPDATE topics SET last_edit_ip = $2 WHERE id = $1`, input.TopicID, input.LastEditIP); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic last edit IP: %w", err)
		}
	}
	if categoryID != state.categoryID {
		if _, err := tx.Exec(ctx, `UPDATE topics SET category_id = $2 WHERE id = $1`, input.TopicID, categoryID); err != nil {
			return TopicDetail{}, fmt.Errorf("update topic category: %w", err)
		}
	}
	if input.TagSlugs != nil && !slices.Equal(state.tags, final.tags) {
		if err := replaceTopicTags(ctx, tx, input.TopicID, final.tags, input.TagCreationMode, input.EditorUserID); err != nil {
			if isRestoreOperation(input.Operation) && errors.Is(err, ErrInvalidTag) {
				return TopicDetail{}, ErrRevisionTagUnavailable
			}
			return TopicDetail{}, err
		}
	}
	if input.ReplaceAttachments && !slices.Equal(state.attachments, final.attachments) {
		if err := replaceRestoreOrForumAttachmentReferences(ctx, tx, "topic", input.TopicID, input.EditorUserID, input.HistoricalAttachmentOwnerID, final.attachments); err != nil {
			return TopicDetail{}, err
		}
	}
	if input.RequeuePending {
		triggers, err := json.Marshal(input.ModerationTriggers)
		if err != nil {
			return TopicDetail{}, fmt.Errorf("encode topic moderation triggers: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE topics SET status = 'pending', moderation_triggers = $2, updated_at = now() WHERE id = $1`, input.TopicID, triggers); err != nil {
			return TopicDetail{}, fmt.Errorf("requeue topic pending: %w", err)
		}
		if state.status == TopicStatusActive {
			if _, err := tx.Exec(ctx, `UPDATE categories SET topic_count = GREATEST(topic_count - 1, 0), updated_at = now() WHERE id = $1`, categoryID); err != nil {
				return TopicDetail{}, fmt.Errorf("decrement topic category after requeue: %w", err)
			}
		}
	}

	revisionNo := state.currentRevision + 1
	revisionID, err := insertAcceptedPostRevision(ctx, tx, AcceptedRevisionSnapshotInput{
		PostID: state.postID, RevisionNo: revisionNo, ActorUserID: input.EditorUserID,
		Operation: revisionOperation(input.Operation), Origin: input.Origin, ChangedFields: changed,
		AttachmentIDs: final.attachments, SnapshotComplete: true, Reason: input.Reason, Content: final.content,
		RestoredFromRevisionID: nullableRevisionID(input.RestoredFromRevisionID),
		Topic:                  &TopicRevisionSnapshotInput{TopicID: input.TopicID, Title: final.title, CategorySlug: final.categorySlug, TagSlugs: final.tags},
	})
	if err != nil {
		return TopicDetail{}, err
	}
	if err := setPostCurrentRevision(ctx, tx, state.postID, revisionNo); err != nil {
		return TopicDetail{}, err
	}
	if err := s.appendForumEditAudit(ctx, tx, "topic", input, revisionID, revisionNo, changed); err != nil {
		return TopicDetail{}, err
	}
	if input.RequeuePending && s.notifications != nil {
		if err := s.notifications.NotifyPendingReviewTx(ctx, tx, PendingReviewNotificationInput{TargetType: "topic", TargetID: input.TopicID, TopicID: input.TopicID, AuthorUserID: input.EditorUserID, Revision: revisionNo, Title: final.title}); err != nil {
			return TopicDetail{}, fmt.Errorf("create requeued topic notifications: %w", err)
		}
	}
	topic, err := readTopicForWriteTx(ctx, tx, s, input.TopicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic.UpdateApplied = true
	topic.UpdateChangedFields = changed
	if err := tx.Commit(ctx); err != nil {
		return TopicDetail{}, fmt.Errorf("commit versioned topic update: %w", err)
	}
	return topic, nil
}

func (s *PostgresStore) UpdateComment(ctx context.Context, input UpdateCommentRecord) (Comment, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Comment{}, fmt.Errorf("begin versioned comment update: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	state, err := lockCommentEditState(ctx, tx, input.CommentID)
	if err != nil {
		return Comment{}, err
	}
	if state.currentRevision != input.ExpectedRevision {
		return Comment{}, ErrRevisionConflict
	}
	if err := validateRestoreSourceTx(ctx, tx, state.postID, input.RestoredFromRevisionID); err != nil {
		return Comment{}, err
	}
	state.attachments, err = attachmentIDsTx(ctx, tx, "comment", input.CommentID)
	if err != nil {
		return Comment{}, err
	}
	finalContent := input.Content
	finalAttachments := state.attachments
	if input.ReplaceAttachments {
		finalAttachments = normalizeRevisionInt64Array(input.AttachmentIDs)
	}
	if isRestoreOperation(input.Operation) && input.ReplaceAttachments {
		if err := validateRestoreAttachmentSnapshotTx(ctx, tx, input.RestoredFromRevisionID, finalAttachments); err != nil {
			return Comment{}, err
		}
		if err := validateHistoricalAttachmentRebindTx(ctx, tx, finalAttachments, input.HistoricalAttachmentOwnerID); err != nil {
			return Comment{}, err
		}
	}
	contentChanged := !sameRevisionContent(state.content, finalContent)
	attachmentsChanged := !slices.Equal(state.attachments, finalAttachments)
	if !contentChanged && !attachmentsChanged && !isRestoreOperation(input.Operation) {
		comment, err := getCommentByID(ctx, tx, input.CommentID, s.avatarBuilder)
		if err != nil {
			return Comment{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Comment{}, fmt.Errorf("commit comment no-op: %w", err)
		}
		return comment, nil
	}
	if contentChanged {
		if err := updatePost(ctx, tx, state.postID, input.EditorUserID, finalContent); err != nil {
			return Comment{}, err
		}
	}
	if input.RequeuePending {
		triggers, err := json.Marshal(input.ModerationTriggers)
		if err != nil {
			return Comment{}, fmt.Errorf("encode comment moderation triggers: %w", err)
		}
		if _, err := tx.Exec(ctx, `UPDATE comments SET status = 'pending', moderation_triggers = $2, updated_at = now() WHERE id = $1`, input.CommentID, triggers); err != nil {
			return Comment{}, fmt.Errorf("requeue comment pending: %w", err)
		}
		if state.status == CommentStatusActive {
			if state.parentID != nil {
				if _, err := tx.Exec(ctx, `UPDATE comments SET reply_count = GREATEST(reply_count - 1, 0), updated_at = now() WHERE id = $1`, *state.parentID); err != nil {
					return Comment{}, fmt.Errorf("decrement parent replies: %w", err)
				}
			}
			if _, err := tx.Exec(ctx, `UPDATE topics SET comment_count = GREATEST(comment_count - 1, 0), hot_score = GREATEST(comment_count - 1, 0) * 5 + view_count, updated_at = now() WHERE id = $1`, state.topicID); err != nil {
				return Comment{}, fmt.Errorf("decrement topic comments: %w", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE categories SET comment_count = GREATEST(comment_count - 1, 0), updated_at = now() WHERE id = (SELECT category_id FROM topics WHERE id = $1)`, state.topicID); err != nil {
				return Comment{}, fmt.Errorf("decrement category comments: %w", err)
			}
		}
	} else if _, err := tx.Exec(ctx, `UPDATE comments SET updated_at = now() WHERE id = $1`, input.CommentID); err != nil {
		return Comment{}, fmt.Errorf("touch comment: %w", err)
	}
	if input.LastEditIP != "" {
		if _, err := tx.Exec(ctx, `UPDATE comments SET last_edit_ip = $2 WHERE id = $1`, input.CommentID, input.LastEditIP); err != nil {
			return Comment{}, fmt.Errorf("update comment last edit IP: %w", err)
		}
	}
	if attachmentsChanged {
		if err := replaceRestoreOrForumAttachmentReferences(ctx, tx, "comment", input.CommentID, input.EditorUserID, input.HistoricalAttachmentOwnerID, finalAttachments); err != nil {
			return Comment{}, err
		}
	}
	changed := []string{}
	if contentChanged {
		changed = append(changed, "content")
	}
	if attachmentsChanged {
		changed = append(changed, "attachments")
	}
	slices.Sort(changed)
	revisionNo := state.currentRevision + 1
	revisionID, err := insertAcceptedPostRevision(ctx, tx, AcceptedRevisionSnapshotInput{PostID: state.postID, RevisionNo: revisionNo, ActorUserID: input.EditorUserID, Operation: revisionOperation(input.Operation), Origin: input.Origin, ChangedFields: changed, AttachmentIDs: finalAttachments, SnapshotComplete: true, Reason: input.Reason, Content: finalContent, RestoredFromRevisionID: nullableRevisionID(input.RestoredFromRevisionID)})
	if err != nil {
		return Comment{}, err
	}
	if err := setPostCurrentRevision(ctx, tx, state.postID, revisionNo); err != nil {
		return Comment{}, err
	}
	if err := s.appendForumCommentEditAudit(ctx, tx, input, revisionID, revisionNo, changed); err != nil {
		return Comment{}, err
	}
	if input.RequeuePending && s.notifications != nil {
		if err := s.notifications.NotifyPendingReviewTx(ctx, tx, PendingReviewNotificationInput{TargetType: "comment", TargetID: input.CommentID, TopicID: state.topicID, AuthorUserID: input.EditorUserID, Revision: revisionNo}); err != nil {
			return Comment{}, fmt.Errorf("create requeued comment notifications: %w", err)
		}
	}
	comment, err := getCommentByID(ctx, tx, input.CommentID, s.avatarBuilder)
	if err != nil {
		return Comment{}, err
	}
	comment.UpdateApplied = true
	comment.UpdateChangedFields = changed
	if err := tx.Commit(ctx); err != nil {
		return Comment{}, fmt.Errorf("commit versioned comment update: %w", err)
	}
	return comment, nil
}

type topicEditState struct {
	topicID, postID, categoryID, currentRevision int64
	title, categorySlug, status                  string
	content                                      RenderedContent
	tags                                         []string
	attachments                                  []int64
}
type commentEditState struct {
	postID, topicID, currentRevision int64
	status                           string
	parentID                         *int64
	content                          RenderedContent
	attachments                      []int64
}

func lockTopicEditState(ctx context.Context, tx pgx.Tx, topicID int64) (topicEditState, error) {
	var state topicEditState
	err := tx.QueryRow(ctx, `SELECT topics.id, topics.content_id, topics.category_id, categories.slug, topics.title, topics.status, posts.current_revision, posts.raw_content, posts.html_content, posts.plain_text, posts.source_format, posts.editor_type, posts.editor_version, posts.render_version, posts.content_hash FROM topics JOIN categories ON categories.id = topics.category_id JOIN posts ON posts.id = topics.content_id WHERE topics.id = $1 AND topics.status <> 'deleted' FOR UPDATE OF topics, posts`, topicID).Scan(&state.topicID, &state.postID, &state.categoryID, &state.categorySlug, &state.title, &state.status, &state.currentRevision, &state.content.RawContent, &state.content.HTMLContent, &state.content.PlainText, &state.content.SourceFormat, &state.content.EditorType, &state.content.EditorVersion, &state.content.RenderVersion, &state.content.ContentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return topicEditState{}, ErrTopicNotFound
	}
	if err != nil {
		return topicEditState{}, fmt.Errorf("lock topic for versioned update: %w", err)
	}
	state.content.ID = state.postID
	return state, nil
}

func lockCommentEditState(ctx context.Context, tx pgx.Tx, commentID int64) (commentEditState, error) {
	var state commentEditState
	err := tx.QueryRow(ctx, `SELECT comments.content_id, comments.topic_id, comments.parent_comment_id, comments.status, posts.current_revision, posts.raw_content, posts.html_content, posts.plain_text, posts.source_format, posts.editor_type, posts.editor_version, posts.render_version, posts.content_hash FROM comments JOIN posts ON posts.id = comments.content_id WHERE comments.id = $1 AND comments.status <> 'deleted' FOR UPDATE OF comments, posts`, commentID).Scan(&state.postID, &state.topicID, &state.parentID, &state.status, &state.currentRevision, &state.content.RawContent, &state.content.HTMLContent, &state.content.PlainText, &state.content.SourceFormat, &state.content.EditorType, &state.content.EditorVersion, &state.content.RenderVersion, &state.content.ContentHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return commentEditState{}, ErrCommentNotFound
	}
	if err != nil {
		return commentEditState{}, fmt.Errorf("lock comment for versioned update: %w", err)
	}
	state.content.ID = state.postID
	return state, nil
}

func topicTagSlugsTx(ctx context.Context, tx pgx.Tx, topicID int64) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT tags.slug FROM topic_tags JOIN tags ON tags.id = topic_tags.tag_id WHERE topic_tags.topic_id = $1 ORDER BY tags.slug ASC`, topicID)
	if err != nil {
		return nil, fmt.Errorf("load topic tags for revision: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var slug string
		if err := rows.Scan(&slug); err != nil {
			return nil, err
		}
		out = append(out, slug)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate topic tags for revision: %w", err)
	}
	return out, nil
}

func attachmentIDsTx(ctx context.Context, tx pgx.Tx, resourceType string, resourceID int64) ([]int64, error) {
	// 正文引用可以有多行（一条内容多张图），因此必须按 context 收敛，不能只按
	// (resource_type, resource_id) 取；DISTINCT 防止同一附件因历史数据重复而污染快照。
	rows, err := tx.Query(ctx, `SELECT DISTINCT attachment_id FROM attachment_references WHERE resource_type = $1 AND resource_id = $2 AND context = $3 ORDER BY attachment_id ASC`, resourceType, resourceID, forumAttachmentContext)
	if err != nil {
		return nil, fmt.Errorf("load revision attachments: %w", err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate revision attachments: %w", err)
	}
	return out, nil
}

func changedTopicSnapshotFields(before, after topicEditState) []string {
	fields := []string{}
	if before.title != after.title {
		fields = append(fields, "title")
	}
	if before.categorySlug != after.categorySlug {
		fields = append(fields, "category")
	}
	if !slices.Equal(before.tags, after.tags) {
		fields = append(fields, "tags")
	}
	if !sameRevisionContent(before.content, after.content) {
		fields = append(fields, "content")
	}
	if !slices.Equal(before.attachments, after.attachments) {
		fields = append(fields, "attachments")
	}
	slices.Sort(fields)
	return fields
}

func sameRevisionContent(a, b RenderedContent) bool {
	return a.RawContent == b.RawContent && a.SourceFormat == b.SourceFormat && a.EditorType == b.EditorType && a.EditorVersion == b.EditorVersion && a.RenderVersion == b.RenderVersion && a.ContentHash == b.ContentHash
}

func readTopicForWriteTx(ctx context.Context, tx pgx.Tx, store *PostgresStore, topicID int64) (TopicDetail, error) {
	topic, err := scanTopicDetailWithAvatar(tx.QueryRow(ctx, topicDetailSQL()+` WHERE topics.id = $1`, topicID), store.avatarBuilder)
	if err != nil {
		return TopicDetail{}, fmt.Errorf("read versioned topic update: %w", err)
	}
	tags, err := topicTagSlugsTx(ctx, tx, topicID)
	if err != nil {
		return TopicDetail{}, err
	}
	topic.Tags = make([]TopicTagSummary, 0, len(tags))
	for _, slug := range tags {
		topic.Tags = append(topic.Tags, TopicTagSummary{Slug: slug, Name: slug, Status: TagStatusActive})
	}
	return topic, nil
}

func (s *PostgresStore) appendForumEditAudit(ctx context.Context, tx pgx.Tx, targetType string, input UpdateTopicRecord, revisionID, revisionNo int64, changed []string) error {
	operation := revisionOperation(input.Operation)
	if (input.Origin != RevisionOriginStaff && operation != RevisionOperationRestore) || s.auditor == nil {
		return nil
	}
	action := audit.ActionForumTopicEditAny
	if operation == RevisionOperationRestore {
		action = audit.ActionForumTopicRevisionRestore
	}
	return s.auditor.AppendTx(ctx, tx, audit.Event{ActorUserID: input.EditorUserID, TargetUserID: input.AuthorUserID, Action: action, Metadata: map[string]any{"targetType": targetType, "targetId": input.TopicID, "authorUserId": input.AuthorUserID, "revisionId": revisionID, "revisionNo": revisionNo, "operation": operation, "changedFields": changed}})
}

func (s *PostgresStore) appendForumCommentEditAudit(ctx context.Context, tx pgx.Tx, input UpdateCommentRecord, revisionID, revisionNo int64, changed []string) error {
	operation := revisionOperation(input.Operation)
	if (input.Origin != RevisionOriginStaff && operation != RevisionOperationRestore) || s.auditor == nil {
		return nil
	}
	action := audit.ActionForumCommentEditAny
	if operation == RevisionOperationRestore {
		action = audit.ActionForumCommentRevisionRestore
	}
	return s.auditor.AppendTx(ctx, tx, audit.Event{ActorUserID: input.EditorUserID, TargetUserID: input.AuthorUserID, Action: action, Metadata: map[string]any{"targetType": "comment", "targetId": input.CommentID, "authorUserId": input.AuthorUserID, "revisionId": revisionID, "revisionNo": revisionNo, "operation": operation, "changedFields": changed}})
}

func revisionOperation(operation string) string {
	if operation == RevisionOperationRestore {
		return RevisionOperationRestore
	}
	return RevisionOperationEdit
}

func isRestoreOperation(operation string) bool {
	return revisionOperation(operation) == RevisionOperationRestore
}

func nullableRevisionID(id int64) *int64 {
	if id <= 0 {
		return nil
	}
	return &id
}

// Restore reads the payload before filters run, then locks and rechecks it here
// so a concurrent redaction cannot be bypassed with a stale in-memory snapshot.
func validateRestoreSourceTx(ctx context.Context, tx pgx.Tx, postID, revisionID int64) error {
	if revisionID <= 0 {
		return nil
	}
	var redacted bool
	err := tx.QueryRow(ctx, `
		SELECT redacted_at IS NOT NULL
		FROM post_revisions
		WHERE id = $1 AND post_id = $2
		FOR UPDATE
	`, revisionID, postID).Scan(&redacted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRevisionNotFound
	}
	if err != nil {
		return fmt.Errorf("lock restore source revision: %w", err)
	}
	if redacted {
		return ErrRevisionRedacted
	}
	return nil
}

func replaceRestoreOrForumAttachmentReferences(ctx context.Context, tx pgx.Tx, resourceType string, resourceID, actorID, historicalOwnerID int64, attachmentIDs []int64) error {
	if historicalOwnerID <= 0 {
		return replaceForumAttachmentReferences(ctx, tx, resourceType, resourceID, actorID, attachmentIDs)
	}
	if err := validateHistoricalAttachmentRebindTx(ctx, tx, attachmentIDs, historicalOwnerID); err != nil {
		return err
	}
	return replaceForumAttachmentReferences(ctx, tx, resourceType, resourceID, historicalOwnerID, attachmentIDs)
}

func validateHistoricalAttachmentRebindTx(ctx context.Context, tx pgx.Tx, attachmentIDs []int64, ownerID int64) error {
	if len(attachmentIDs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id FROM attachments
		WHERE id = ANY($1::bigint[]) AND owner_user_id = $2
		  AND status = 'active' AND visibility = 'public'
		FOR UPDATE
	`, attachmentIDs, ownerID)
	if err != nil {
		return fmt.Errorf("validate historical attachment restore: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate historical attachments: %w", err)
	}
	if count != len(attachmentIDs) {
		return ErrRevisionAttachmentUnavailable
	}
	return nil
}

func validateRestoreAttachmentSnapshotTx(ctx context.Context, tx pgx.Tx, revisionID int64, attachmentIDs []int64) error {
	var snapshot []int64
	if err := tx.QueryRow(ctx, `SELECT attachment_ids FROM post_revisions WHERE id = $1`, revisionID).Scan(&snapshot); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrRevisionNotFound
		}
		return fmt.Errorf("load historical attachment snapshot: %w", err)
	}
	if !slices.Equal(normalizeRevisionInt64Array(snapshot), normalizeRevisionInt64Array(attachmentIDs)) {
		return ErrRevisionAttachmentUnavailable
	}
	return nil
}

// A restore may target the same tag set currently attached to a topic. Validate
// it anyway: a tag could have been disabled after the snapshot was accepted.
func validateHistoricalTopicTagsTx(ctx context.Context, tx pgx.Tx, slugs []string, creationMode string) error {
	if len(slugs) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT slug, status FROM tags WHERE slug = ANY($1::text[]) FOR UPDATE`, slugs)
	if err != nil {
		return fmt.Errorf("validate historical topic tags: %w", err)
	}
	defer rows.Close()
	found := 0
	for rows.Next() {
		var slug, status string
		if err := rows.Scan(&slug, &status); err != nil {
			return fmt.Errorf("scan historical topic tag: %w", err)
		}
		if status == TagStatusDisabled || (status == TagStatusPending && creationMode == TagCreationModeControlled) {
			return ErrRevisionTagUnavailable
		}
		found++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate historical topic tags: %w", err)
	}
	if found != len(slugs) {
		return ErrRevisionTagUnavailable
	}
	return nil
}
