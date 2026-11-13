package forum

// 评论读路径（从 postgres_store_ops.go 抽出，逻辑零改动）。
//
// 抽出原因：ops.go 是 legacy 基线文件（cap 只减不增），M3 给徽章补列与扫描
// 目标后它越顶。评论读路径是自洽的一簇（SELECT 构造 + 单行扫描 + 主键读取），
// 整体搬走比继续往里加更符合「贴顶文件先抽再改」的纪律。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

func commentSelectSQL() string {
	return `
		SELECT comments.id, comments.topic_id, comments.author_user_id,
		  users.username, users.display_name, users.email,
		  author_profiles.avatar_attachment_id,
		  author_attachments.id, author_attachments.public_id, author_attachments.owner_user_id,
		  author_attachments.content_type, author_attachments.status,
		  comments.parent_comment_id, COALESCE(comments.root_comment_id, comments.id),
		  comments.path_key, comments.depth, comments.reply_count, comments.like_count, comments.status,
		  posts.id, posts.html_content, posts.plain_text, posts.render_version,
		  parent_comments.id,
		  CASE WHEN parent_comments.status = 'deleted' THEN '' ELSE ` + plainTextPrefixSQL("parent_posts.plain_text") + ` END,
		  parent_comments.depth,
		  parent_users.id, parent_users.username, parent_users.display_name, parent_users.email,
		  parent_profiles.avatar_attachment_id,
		  parent_attachments.id, parent_attachments.public_id, parent_attachments.owner_user_id,
		  parent_attachments.content_type, parent_attachments.status,
		  ` + effectivePostCurrentRevisionSQL("posts") + `,
		  ` + contentEditedSQL("posts") + `,
		  ` + contentEditedAtSQL("posts") + `,
		  comments.created_at, comments.updated_at,
		  ` + topBadgeSelectSQL() + `
		FROM comments
		JOIN posts ON posts.id = comments.content_id
		LEFT JOIN users ON users.id = comments.author_user_id
		LEFT JOIN user_profiles author_profiles ON author_profiles.user_id = users.id
		LEFT JOIN attachments author_attachments ON author_attachments.id = author_profiles.avatar_attachment_id
		LEFT JOIN comments parent_comments ON parent_comments.id = comments.parent_comment_id
		LEFT JOIN posts parent_posts ON parent_posts.id = parent_comments.content_id
		LEFT JOIN users parent_users ON parent_users.id = parent_comments.author_user_id
		LEFT JOIN user_profiles parent_profiles ON parent_profiles.user_id = parent_users.id
		LEFT JOIN attachments parent_attachments ON parent_attachments.id = parent_profiles.avatar_attachment_id` + topBadgeJoinSQL() + `
	`
}

func getCommentByID(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, commentID int64, builder *avatar.ViewBuilder) (Comment, error) {
	row := q.QueryRow(ctx, commentSelectSQL()+`
		WHERE comments.id = $1
	`, commentID)
	comment, err := scanCommentWithAvatar(row, builder)
	if errors.Is(err, pgx.ErrNoRows) {
		return Comment{}, ErrCommentNotFound
	}
	if err != nil {
		return Comment{}, fmt.Errorf("get comment: %w", err)
	}
	return comment, nil
}

func scanComment(row RowScanner) (Comment, error) {
	return scanCommentWithAvatar(row, nil)
}

func scanCommentWithAvatar(row RowScanner, builder *avatar.ViewBuilder) (Comment, error) {
	var comment Comment
	var authorID sql.NullInt64
	var username sql.NullString
	var displayName sql.NullString
	var email sql.NullString
	var avatarAttachmentID sql.NullInt64
	var attachmentID sql.NullInt64
	var attachmentPublicID sql.NullString
	var attachmentOwnerID sql.NullInt64
	var attachmentContentType sql.NullString
	var attachmentStatus sql.NullString
	var parentID sql.NullInt64
	var parentCommentID sql.NullInt64
	var parentPlainPrefix sql.NullString
	var parentDepth sql.NullInt64
	var parentAuthorID sql.NullInt64
	var parentUsername sql.NullString
	var parentDisplayName sql.NullString
	var parentEmail sql.NullString
	var parentAvatarAttachmentID sql.NullInt64
	var parentAttachmentID sql.NullInt64
	var parentAttachmentPublicID sql.NullString
	var parentAttachmentOwnerID sql.NullInt64
	var parentAttachmentContentType sql.NullString
	var parentAttachmentStatus sql.NullString

	var badgeSlug, badgeName, badgeTier, badgeIcon sql.NullString
	var badgeTierNameZh, badgeTierNameEn, badgeTierColor sql.NullString
	if err := row.Scan(
		&comment.ID,
		&comment.TopicID,
		&authorID,
		&username,
		&displayName,
		&email,
		&avatarAttachmentID,
		&attachmentID,
		&attachmentPublicID,
		&attachmentOwnerID,
		&attachmentContentType,
		&attachmentStatus,
		&parentID,
		&comment.RootCommentID,
		&comment.PathKey,
		&comment.Depth,
		&comment.ReplyCount,
		&comment.LikeCount,
		&comment.Status,
		&comment.Content.ID,
		&comment.Content.HTMLContent,
		&comment.Content.PlainText,
		&comment.Content.RenderVersion,
		&parentCommentID,
		&parentPlainPrefix,
		&parentDepth,
		&parentAuthorID,
		&parentUsername,
		&parentDisplayName,
		&parentEmail,
		&parentAvatarAttachmentID,
		&parentAttachmentID,
		&parentAttachmentPublicID,
		&parentAttachmentOwnerID,
		&parentAttachmentContentType,
		&parentAttachmentStatus,
		&comment.CurrentRevision,
		&comment.ContentEdited,
		&comment.EditedAt,
		&comment.CreatedAt,
		&comment.UpdatedAt,
		&badgeSlug,
		&badgeName,
		&badgeTier,
		&badgeIcon,
		&badgeTierNameZh,
		&badgeTierNameEn,
		&badgeTierColor,
	); err != nil {
		return Comment{}, err
	}
	comment.Content.Excerpt = ExcerptFromPlain(comment.Content.PlainText, defaultExcerptRuneLimit)
	comment.TopBadge = badgeRefFromNulls(badgeSlug, badgeName, badgeTier, badgeIcon, badgeTierNameZh, badgeTierNameEn, badgeTierColor)
	if authorID.Valid {
		comment.AuthorUserID = authorID.Int64
		comment.Author = userSummaryWithAvatar(builder, authorID, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
	}
	if parentID.Valid {
		comment.ParentID = &parentID.Int64
	}
	if parentCommentID.Valid {
		replyTo := &ReplyReference{
			ID:      parentCommentID.Int64,
			Excerpt: ExcerptFromPlain(parentPlainPrefix.String, defaultExcerptRuneLimit),
			Depth:   int(parentDepth.Int64),
		}
		if parentAuthorID.Valid {
			replyTo.Author = userSummaryWithAvatar(builder, parentAuthorID, parentUsername, parentDisplayName, parentEmail, parentAvatarAttachmentID, parentAttachmentID, parentAttachmentPublicID, parentAttachmentOwnerID, parentAttachmentContentType, parentAttachmentStatus)
		}
		comment.ReplyTo = replyTo
	}
	return comment, nil
}
