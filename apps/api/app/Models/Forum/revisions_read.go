package forum

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

const (
	revisionDefaultPerPage = 20
	revisionMaxPerPage     = 100
)

func normalizeRevisionPerPage(perPage int) int {
	if perPage <= 0 {
		return revisionDefaultPerPage
	}
	if perPage > revisionMaxPerPage {
		return revisionMaxPerPage
	}
	return perPage
}

func revisionSummarySelectSQL() string {
	return `
		SELECT post_revisions.id, post_revisions.revision_no,
		  post_revisions.revision_no = posts.current_revision,
		  post_revisions.actor_user_id, users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  COALESCE(post_revisions.operation, 'migration'),
		  COALESCE(post_revisions.origin, 'migration'),
		  post_revisions.reason,
		  post_revisions.changed_fields,
		  COALESCE(post_revisions.committed_at, post_revisions.created_at),
		  restored.revision_no,
		  post_revisions.snapshot_complete,
		  post_revisions.redacted_at IS NOT NULL
		FROM post_revisions
		JOIN posts ON posts.id = post_revisions.post_id
		LEFT JOIN users ON users.id = post_revisions.actor_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
		LEFT JOIN post_revisions restored ON restored.id = post_revisions.restored_from_revision_id
	`
}

func scanRevisionSummary(row RowScanner, builder *avatar.ViewBuilder, targetType string) (ForumRevisionSummary, error) {
	var summary ForumRevisionSummary
	var actorID sql.NullInt64
	var username, displayName, email sql.NullString
	var avatarAttachmentID, attachmentID, attachmentOwnerID sql.NullInt64
	var attachmentPublicID, attachmentContentType, attachmentStatus sql.NullString
	var restoredFrom sql.NullInt64
	if err := row.Scan(
		&summary.ID,
		&summary.RevisionNo,
		&summary.Current,
		&actorID,
		&username,
		&displayName,
		&email,
		&avatarAttachmentID,
		&attachmentID,
		&attachmentPublicID,
		&attachmentOwnerID,
		&attachmentContentType,
		&attachmentStatus,
		&summary.Operation,
		&summary.Origin,
		&summary.Reason,
		&summary.ChangedFields,
		&summary.CommittedAt,
		&restoredFrom,
		&summary.SnapshotComplete,
		&summary.Redacted,
	); err != nil {
		return ForumRevisionSummary{}, err
	}
	if actorID.Valid {
		summary.Actor = userSummaryWithAvatar(builder, actorID, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
	}
	if restoredFrom.Valid {
		value := restoredFrom.Int64
		summary.RestoredFromRevisionNo = &value
	}
	summary.RestorableFields = revisionRestorableFields(targetType, summary.SnapshotComplete, summary.Redacted)
	return summary, nil
}

func revisionRestorableFields(targetType string, complete bool, redacted bool) []string {
	if redacted {
		return []string{}
	}
	if !complete {
		return []string{"content"}
	}
	if targetType == "topic" {
		return []string{"attachments", "category", "content", "tags", "title"}
	}
	return []string{"attachments", "content"}
}

func (s *PostgresStore) ListTopicRevisions(ctx context.Context, topicID int64, input RevisionListInput) (RevisionList, error) {
	postID, err := s.topicPostID(ctx, topicID)
	if err != nil {
		return RevisionList{}, err
	}
	return s.listPostRevisions(ctx, postID, "topic", input)
}

func (s *PostgresStore) ListCommentRevisions(ctx context.Context, commentID int64, input RevisionListInput) (RevisionList, error) {
	postID, err := s.commentPostID(ctx, commentID)
	if err != nil {
		return RevisionList{}, err
	}
	return s.listPostRevisions(ctx, postID, "comment", input)
}

func (s *PostgresStore) listPostRevisions(ctx context.Context, postID int64, targetType string, input RevisionListInput) (RevisionList, error) {
	perPage := normalizeRevisionPerPage(input.PerPage)
	args := []any{postID}
	where := `WHERE post_revisions.post_id = $1 AND post_revisions.revision_no IS NOT NULL`
	if strings.TrimSpace(input.After) != "" {
		cursor, err := decodeRevisionListCursor(input.After)
		if err != nil {
			return RevisionList{}, err
		}
		args = append(args, cursor.RevisionNo, cursor.ID)
		where += ` AND (post_revisions.revision_no < $2 OR (post_revisions.revision_no = $2 AND post_revisions.id < $3))`
	}
	args = append(args, perPage+1)
	query := revisionSummarySelectSQL() + `
		` + where + `
		ORDER BY post_revisions.revision_no DESC, post_revisions.id DESC
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return RevisionList{}, fmt.Errorf("list post revisions: %w", err)
	}
	defer rows.Close()
	items := make([]ForumRevisionSummary, 0, perPage)
	for rows.Next() {
		item, err := scanRevisionSummary(rows, s.avatarBuilder, targetType)
		if err != nil {
			return RevisionList{}, fmt.Errorf("scan post revision summary: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return RevisionList{}, fmt.Errorf("iterate post revisions: %w", err)
	}
	hasMore := len(items) > perPage
	if hasMore {
		items = items[:perPage]
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor, err = encodeRevisionListCursor(revisionListCursor{RevisionNo: last.RevisionNo, ID: last.ID})
		if err != nil {
			return RevisionList{}, err
		}
	}
	return RevisionList{Items: items, PerPage: perPage, HasMore: hasMore, NextCursor: nextCursor}, nil
}

func (s *PostgresStore) GetTopicRevision(ctx context.Context, topicID int64, revisionNo int64) (ForumRevisionDetail, error) {
	postID, err := s.topicPostID(ctx, topicID)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	return s.getPostRevision(ctx, postID, revisionNo, "topic")
}

func (s *PostgresStore) GetCommentRevision(ctx context.Context, commentID int64, revisionNo int64) (ForumRevisionDetail, error) {
	postID, err := s.commentPostID(ctx, commentID)
	if err != nil {
		return ForumRevisionDetail{}, err
	}
	return s.getPostRevision(ctx, postID, revisionNo, "comment")
}

func (s *PostgresStore) getPostRevision(ctx context.Context, postID int64, revisionNo int64, targetType string) (ForumRevisionDetail, error) {
	if revisionNo <= 0 {
		return ForumRevisionDetail{}, ErrRevisionNotFound
	}
	row := s.pool.QueryRow(ctx, revisionSummarySelectSQL()+`
		WHERE post_revisions.post_id = $1 AND post_revisions.revision_no = $2
	`, postID, revisionNo)
	summary, err := scanRevisionSummary(row, s.avatarBuilder, targetType)
	if errors.Is(err, pgx.ErrNoRows) {
		return ForumRevisionDetail{}, ErrRevisionNotFound
	}
	if err != nil {
		return ForumRevisionDetail{}, fmt.Errorf("get post revision summary: %w", err)
	}
	if summary.Redacted {
		return ForumRevisionDetail{}, ErrRevisionRedacted
	}
	var detail ForumRevisionDetail
	detail.ForumRevisionSummary = summary
	var attachmentIDs []int64
	if err := s.pool.QueryRow(ctx, `
		SELECT raw_content, source_format, editor_type, editor_version, render_version,
		  content_hash, attachment_ids
		FROM post_revisions
		WHERE id = $1
	`, summary.ID).Scan(
		&detail.RawContent,
		&detail.SourceFormat,
		&detail.EditorType,
		&detail.EditorVersion,
		&detail.RenderVersion,
		&detail.ContentHash,
		&attachmentIDs,
	); err != nil {
		return ForumRevisionDetail{}, fmt.Errorf("get post revision detail: %w", err)
	}
	detail.Attachments = AttachmentAvailabilitySummary{IDs: normalizeRevisionInt64Array(attachmentIDs), Total: len(attachmentIDs)}
	if targetType == "topic" {
		meta, err := s.topicRevisionMetadata(ctx, summary.ID)
		if err != nil {
			return ForumRevisionDetail{}, err
		}
		detail.TopicMetadata = meta
	}
	return detail, nil
}

func (s *PostgresStore) topicPostID(ctx context.Context, topicID int64) (int64, error) {
	var postID int64
	if err := s.pool.QueryRow(ctx, `SELECT content_id FROM topics WHERE id = $1`, topicID).Scan(&postID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrTopicNotFound
		}
		return 0, fmt.Errorf("load topic post id: %w", err)
	}
	return postID, nil
}

func (s *PostgresStore) commentPostID(ctx context.Context, commentID int64) (int64, error) {
	var postID int64
	if err := s.pool.QueryRow(ctx, `SELECT content_id FROM comments WHERE id = $1`, commentID).Scan(&postID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ErrCommentNotFound
		}
		return 0, fmt.Errorf("load comment post id: %w", err)
	}
	return postID, nil
}

func (s *PostgresStore) topicRevisionMetadata(ctx context.Context, revisionID int64) (*TopicRevisionMetadata, error) {
	var meta TopicRevisionMetadata
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(title, ''), COALESCE(category_slug, ''), tag_slugs
		FROM topic_revision_snapshots
		WHERE post_revision_id = $1
	`, revisionID).Scan(&meta.Title, &meta.CategorySlug, &meta.TagSlugs); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("load topic revision metadata: %w", err)
	}
	meta.TagSlugs = normalizeRevisionTextArray(meta.TagSlugs)
	return &meta, nil
}

func (s *PostgresStore) ListAdminForumTopics(ctx context.Context, input AdminForumContentListInput) (AdminForumContentList, error) {
	return s.listAdminForumContent(ctx, "topic", input)
}

func (s *PostgresStore) ListAdminForumComments(ctx context.Context, input AdminForumContentListInput) (AdminForumContentList, error) {
	return s.listAdminForumContent(ctx, "comment", input)
}

func (s *PostgresStore) listAdminForumContent(ctx context.Context, targetType string, input AdminForumContentListInput) (AdminForumContentList, error) {
	perPage := normalizeRevisionPerPage(input.PerPage)
	args := []any{}
	where := []string{"1=1"}
	addArg := func(value any) string {
		args = append(args, value)
		return fmt.Sprintf("$%d", len(args))
	}
	if input.Status = strings.TrimSpace(input.Status); input.Status != "" {
		where = append(where, "resource.status = "+addArg(input.Status))
	}
	if input.AuthorUserID > 0 {
		where = append(where, "resource.author_user_id = "+addArg(input.AuthorUserID))
	}
	if input.AuthorUsername = strings.TrimSpace(input.AuthorUsername); input.AuthorUsername != "" {
		where = append(where, "lower(users.username) = lower("+addArg(input.AuthorUsername)+")")
	}
	if !input.UpdatedFrom.IsZero() {
		where = append(where, "resource.updated_at >= "+addArg(input.UpdatedFrom))
	}
	if !input.UpdatedTo.IsZero() {
		where = append(where, "resource.updated_at <= "+addArg(input.UpdatedTo))
	}
	if input.TopicID > 0 {
		if targetType == "topic" {
			where = append(where, "resource.id = "+addArg(input.TopicID))
		} else {
			where = append(where, "resource.topic_id = "+addArg(input.TopicID))
		}
	}
	if input.TitlePrefix = strings.TrimSpace(input.TitlePrefix); input.TitlePrefix != "" && targetType == "topic" {
		where = append(where, "lower(resource.title) LIKE lower("+addArg(input.TitlePrefix)+") || '%'")
	}
	if input.CategorySlug = strings.TrimSpace(input.CategorySlug); input.CategorySlug != "" && targetType == "topic" {
		where = append(where, "categories.slug = "+addArg(input.CategorySlug))
	}
	if strings.TrimSpace(input.After) != "" {
		cursor, updatedAt, err := decodeAdminContentCursor(input.After)
		if err != nil {
			return AdminForumContentList{}, err
		}
		where = append(where, "(resource.updated_at < "+addArg(updatedAt)+" OR (resource.updated_at = "+addArg(updatedAt)+" AND resource.id < "+addArg(cursor.ID)+"))")
	}
	args = append(args, perPage+1)
	query := adminContentListSQL(targetType) + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY resource.updated_at DESC, resource.id DESC
		LIMIT $` + fmt.Sprint(len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return AdminForumContentList{}, fmt.Errorf("list admin forum %s content: %w", targetType, err)
	}
	defer rows.Close()
	items := make([]AdminForumContentRow, 0, perPage)
	for rows.Next() {
		item, err := scanAdminForumContentRow(rows, s.avatarBuilder)
		if err != nil {
			return AdminForumContentList{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AdminForumContentList{}, fmt.Errorf("iterate admin forum %s content: %w", targetType, err)
	}
	hasMore := len(items) > perPage
	if hasMore {
		items = items[:perPage]
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		last := items[len(items)-1]
		nextCursor, err = encodeAdminContentCursor(last.UpdatedAt, last.ID)
		if err != nil {
			return AdminForumContentList{}, err
		}
	}
	if targetType == "topic" {
		if err := s.decorateAdminTopicRows(ctx, items); err != nil {
			return AdminForumContentList{}, err
		}
	}
	return AdminForumContentList{Items: items, PerPage: perPage, HasMore: hasMore, NextCursor: nextCursor}, nil
}

func adminContentListSQL(targetType string) string {
	if targetType == "comment" {
		return `
			SELECT 'comment'::text, resource.id, resource.topic_id, topics.title, categories.slug,
			  resource.author_user_id, users.username, users.display_name, users.email,
			  author_profiles.avatar_attachment_id,
			  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
			  author_attachments.content_type, author_attachments.status,
			  resource.status, ''::text, ` + plainTextPrefixSQL("posts.plain_text") + `,
			  ` + effectivePostCurrentRevisionSQL("posts") + `,
			  resource.created_at, resource.updated_at
			FROM comments resource
			JOIN topics ON topics.id = resource.topic_id
			JOIN categories ON categories.id = topics.category_id
			JOIN posts ON posts.id = resource.content_id
			LEFT JOIN users ON users.id = resource.author_user_id
			LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
			LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
		`
	}
	return `
		SELECT 'topic'::text, resource.id, resource.id, resource.title, categories.slug,
		  resource.author_user_id, users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  resource.status, resource.title, ` + plainTextPrefixSQL("posts.plain_text") + `,
		  ` + effectivePostCurrentRevisionSQL("posts") + `,
		  resource.created_at, resource.updated_at
		FROM topics resource
		JOIN categories ON categories.id = resource.category_id
		JOIN posts ON posts.id = resource.content_id
		LEFT JOIN users ON users.id = resource.author_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
	`
}

func scanAdminForumContentRow(row RowScanner, builder *avatar.ViewBuilder) (AdminForumContentRow, error) {
	var item AdminForumContentRow
	var actorID sql.NullInt64
	var username, displayName, email sql.NullString
	var avatarAttachmentID, attachmentID, attachmentOwnerID sql.NullInt64
	var attachmentPublicID, attachmentContentType, attachmentStatus sql.NullString
	var plainPrefix string
	if err := row.Scan(
		&item.TargetType,
		&item.ID,
		&item.TopicID,
		&item.TopicTitle,
		&item.CategorySlug,
		&actorID,
		&username,
		&displayName,
		&email,
		&avatarAttachmentID,
		&attachmentID,
		&attachmentPublicID,
		&attachmentOwnerID,
		&attachmentContentType,
		&attachmentStatus,
		&item.Status,
		&item.Title,
		&plainPrefix,
		&item.CurrentRevision,
		&item.CreatedAt,
		&item.UpdatedAt,
	); err != nil {
		return AdminForumContentRow{}, fmt.Errorf("scan admin forum content row: %w", err)
	}
	if actorID.Valid {
		item.AuthorUserID = actorID.Int64
		item.Author = userSummaryWithAvatar(builder, actorID, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
	}
	item.Excerpt = ExcerptFromPlain(plainPrefix, defaultExcerptRuneLimit)
	return item, nil
}

func (s *PostgresStore) decorateAdminTopicRows(ctx context.Context, items []AdminForumContentRow) error {
	topicIDs := make([]int64, 0, len(items))
	for _, item := range items {
		if item.TargetType == "topic" {
			topicIDs = append(topicIDs, item.ID)
		}
	}
	tags, err := s.activeTopicTags(ctx, topicIDs)
	if err != nil {
		return err
	}
	for i := range items {
		items[i].Tags = tags[items[i].ID]
	}
	return nil
}

func (s *PostgresStore) GetAdminForumTopic(ctx context.Context, topicID int64) (AdminForumTopicDetail, error) {
	row := s.pool.QueryRow(ctx, topicDetailSQL()+` WHERE topics.id = $1`, topicID)
	topic, err := scanTopicDetailWithAvatar(row, s.avatarBuilder)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminForumTopicDetail{}, ErrTopicNotFound
	}
	if err != nil {
		return AdminForumTopicDetail{}, fmt.Errorf("get admin forum topic: %w", err)
	}
	tags, err := s.activeTopicTags(ctx, []int64{topic.ID})
	if err != nil {
		return AdminForumTopicDetail{}, err
	}
	topic.Tags = tags[topic.ID]
	content, err := s.getProtectedRenderedContent(ctx, topic.Content.ID)
	if err != nil {
		return AdminForumTopicDetail{}, err
	}
	return AdminForumTopicDetail{
		AdminForumContentRow: AdminForumContentRow{
			TargetType:      "topic",
			ID:              topic.ID,
			TopicID:         topic.ID,
			TopicTitle:      topic.Title,
			CategorySlug:    topic.CategorySlug,
			AuthorUserID:    topic.AuthorUserID,
			Author:          topic.Author,
			Status:          topic.Status,
			Title:           topic.Title,
			Excerpt:         topic.Excerpt,
			CurrentRevision: topic.CurrentRevision,
			CreatedAt:       topic.CreatedAt,
			UpdatedAt:       topic.UpdatedAt,
			Tags:            topic.Tags,
		},
		Content: content,
		Slug:    topic.Slug,
	}, nil
}

func (s *PostgresStore) GetAdminForumComment(ctx context.Context, commentID int64) (AdminForumCommentDetail, error) {
	comment, err := getCommentByID(ctx, s.pool, commentID, s.avatarBuilder)
	if err != nil {
		return AdminForumCommentDetail{}, err
	}
	var topicTitle, categorySlug string
	if err := s.pool.QueryRow(ctx, `
		SELECT topics.title, categories.slug
		FROM topics
		JOIN categories ON categories.id = topics.category_id
		WHERE topics.id = $1
	`, comment.TopicID).Scan(&topicTitle, &categorySlug); err != nil {
		return AdminForumCommentDetail{}, fmt.Errorf("load admin comment topic context: %w", err)
	}
	content, err := s.getProtectedRenderedContent(ctx, comment.Content.ID)
	if err != nil {
		return AdminForumCommentDetail{}, err
	}
	return AdminForumCommentDetail{
		AdminForumContentRow: AdminForumContentRow{
			TargetType:      "comment",
			ID:              comment.ID,
			TopicID:         comment.TopicID,
			TopicTitle:      topicTitle,
			CategorySlug:    categorySlug,
			AuthorUserID:    comment.AuthorUserID,
			Author:          comment.Author,
			Status:          comment.Status,
			Excerpt:         comment.Content.Excerpt,
			CurrentRevision: comment.CurrentRevision,
			CreatedAt:       comment.CreatedAt,
			UpdatedAt:       comment.UpdatedAt,
		},
		Content:       content,
		ParentID:      comment.ParentID,
		RootCommentID: comment.RootCommentID,
		PathKey:       comment.PathKey,
		Depth:         comment.Depth,
		ReplyCount:    comment.ReplyCount,
	}, nil
}

func (s *PostgresStore) getProtectedRenderedContent(ctx context.Context, postID int64) (RenderedContent, error) {
	var content RenderedContent
	err := s.pool.QueryRow(ctx, `
		SELECT id, raw_content, html_content, plain_text, source_format,
		  editor_type, editor_version, render_version, content_hash
		FROM posts
		WHERE id = $1
	`, postID).Scan(
		&content.ID,
		&content.RawContent,
		&content.HTMLContent,
		&content.PlainText,
		&content.SourceFormat,
		&content.EditorType,
		&content.EditorVersion,
		&content.RenderVersion,
		&content.ContentHash,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RenderedContent{}, ErrRevisionNotFound
	}
	if err != nil {
		return RenderedContent{}, fmt.Errorf("get protected rendered content: %w", err)
	}
	content.Excerpt = ExcerptFromPlain(content.PlainText, defaultExcerptRuneLimit)
	return content, nil
}
