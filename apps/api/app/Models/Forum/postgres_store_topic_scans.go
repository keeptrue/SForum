package forum

// 本文件收拢「主题行 → 读模型」的位置扫描器（摘要 / 详情 / 详情带头像）。
//
// 为什么独立成文件：postgres_store_ops.go 是 1100+ 行的历史大文件，架构门禁按行数
// 钉住它（只许缩不许涨），而徽章外观列让这三个扫描器各自多出 3 个扫描目的地。
// 扫描器是纯粹的行→结构映射，与 store 的读写流程没有共享状态；集中在一处也让
// 「列数必须等于目的地数」这条位置契约更容易一次看全。
//
// 列清单的唯一真源仍是 topic_summary_sql.go / topicDetailSQL()，本文件只负责扫描。

import (
	"database/sql"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
)

func scanTopicSummaryWithAvatar(row RowScanner, builder *avatar.ViewBuilder) (TopicSummary, error) {
	var topic TopicSummary
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
	var lastReplyID sql.NullInt64
	var lastReplyUsername sql.NullString
	var lastReplyDisplayName sql.NullString
	var lastReplyEmail sql.NullString
	var lastReplyAvatarAttachmentID sql.NullInt64
	var lastReplyAttachmentID sql.NullInt64
	var lastReplyAttachmentPublicID sql.NullString
	var lastReplyAttachmentOwnerID sql.NullInt64
	var lastReplyAttachmentContentType sql.NullString
	var lastReplyAttachmentStatus sql.NullString
	var plainPrefix string
	var badgeSlug, badgeName, badgeTier, badgeIcon sql.NullString
	var badgeTierNameZh, badgeTierNameEn, badgeTierColor sql.NullString
	if err := row.Scan(
		&topic.ID,
		&topic.CategoryID,
		&topic.CategorySlug,
		&topic.CategoryName,
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
		&topic.Title,
		&topic.Slug,
		&topic.Status,
		&topic.IsPinned,
		&topic.CommentCount,
		&topic.ViewCount,
		&topic.LikeCount,
		&topic.HotScore,
		&plainPrefix,
		&topic.CurrentRevision,
		&topic.ContentEdited,
		&topic.CreatedAt,
		&topic.UpdatedAt,
		&topic.LastActivityAt,
		&lastReplyID,
		&lastReplyUsername,
		&lastReplyDisplayName,
		&lastReplyEmail,
		&lastReplyAvatarAttachmentID,
		&lastReplyAttachmentID,
		&lastReplyAttachmentPublicID,
		&lastReplyAttachmentOwnerID,
		&lastReplyAttachmentContentType,
		&lastReplyAttachmentStatus,
		&badgeSlug,
		&badgeName,
		&badgeTier,
		&badgeIcon,
		&badgeTierNameZh,
		&badgeTierNameEn,
		&badgeTierColor,
	); err != nil {
		return TopicSummary{}, err
	}
	topic.Excerpt = ExcerptFromPlain(plainPrefix, defaultExcerptRuneLimit)
	topic.TopBadge = badgeRefFromNulls(badgeSlug, badgeName, badgeTier, badgeIcon, badgeTierNameZh, badgeTierNameEn, badgeTierColor)
	if authorID.Valid {
		topic.AuthorUserID = authorID.Int64
		topic.Author = userSummaryWithAvatar(builder, authorID, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
	}
	topic.LastReplyAuthor = userSummaryWithAvatar(
		builder,
		lastReplyID,
		lastReplyUsername,
		lastReplyDisplayName,
		lastReplyEmail,
		lastReplyAvatarAttachmentID,
		lastReplyAttachmentID,
		lastReplyAttachmentPublicID,
		lastReplyAttachmentOwnerID,
		lastReplyAttachmentContentType,
		lastReplyAttachmentStatus,
	)
	return topic, nil
}

func scanTopicDetail(row RowScanner) (TopicDetail, error) {
	var detail TopicDetail
	var authorID sql.NullInt64
	var username sql.NullString
	var displayName sql.NullString
	var badgeSlug, badgeName, badgeTier, badgeIcon sql.NullString
	var badgeTierNameZh, badgeTierNameEn, badgeTierColor sql.NullString
	if err := row.Scan(
		&detail.ID,
		&detail.CategoryID,
		&detail.CategorySlug,
		&detail.CategoryName,
		&authorID,
		&username,
		&displayName,
		&detail.Title,
		&detail.Slug,
		&detail.Status,
		&detail.IsPinned,
		&detail.CommentCount,
		&detail.ViewCount,
		&detail.LikeCount,
		&detail.CurrentRevision,
		&detail.ContentEdited,
		&detail.EditedAt,
		&detail.CreatedAt,
		&detail.UpdatedAt,
		&detail.LastActivityAt,
		&detail.Content.ID,
		&detail.Content.HTMLContent,
		&detail.Content.PlainText,
		&detail.Content.RenderVersion,
		&badgeSlug,
		&badgeName,
		&badgeTier,
		&badgeIcon,
		&badgeTierNameZh,
		&badgeTierNameEn,
		&badgeTierColor,
	); err != nil {
		return TopicDetail{}, err
	}
	// excerpt 从 plain_text 派生，详情与 content 字段共用同一摘要。
	detail.Content.Excerpt = ExcerptFromPlain(detail.Content.PlainText, defaultExcerptRuneLimit)
	detail.Excerpt = detail.Content.Excerpt
	detail.TopBadge = badgeRefFromNulls(badgeSlug, badgeName, badgeTier, badgeIcon, badgeTierNameZh, badgeTierNameEn, badgeTierColor)
	if authorID.Valid {
		detail.AuthorUserID = authorID.Int64
		detail.Author = userSummaryWithAvatar(nil, authorID, username, displayName, sql.NullString{}, sql.NullInt64{}, sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{})
	}
	return detail, nil
}

func scanTopicDetailWithAvatar(row RowScanner, builder *avatar.ViewBuilder) (TopicDetail, error) {
	var detail TopicDetail
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
	var badgeSlug, badgeName, badgeTier, badgeIcon sql.NullString
	var badgeTierNameZh, badgeTierNameEn, badgeTierColor sql.NullString
	if err := row.Scan(
		&detail.ID,
		&detail.CategoryID,
		&detail.CategorySlug,
		&detail.CategoryName,
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
		&detail.Title,
		&detail.Slug,
		&detail.Status,
		&detail.IsPinned,
		&detail.CommentCount,
		&detail.ViewCount,
		&detail.LikeCount,
		&detail.CurrentRevision,
		&detail.ContentEdited,
		&detail.EditedAt,
		&detail.CreatedAt,
		&detail.UpdatedAt,
		&detail.LastActivityAt,
		&detail.Content.ID,
		&detail.Content.HTMLContent,
		&detail.Content.PlainText,
		&detail.Content.RenderVersion,
		&badgeSlug,
		&badgeName,
		&badgeTier,
		&badgeIcon,
		&badgeTierNameZh,
		&badgeTierNameEn,
		&badgeTierColor,
	); err != nil {
		return TopicDetail{}, err
	}
	detail.Content.Excerpt = ExcerptFromPlain(detail.Content.PlainText, defaultExcerptRuneLimit)
	detail.Excerpt = detail.Content.Excerpt
	detail.TopBadge = badgeRefFromNulls(badgeSlug, badgeName, badgeTier, badgeIcon, badgeTierNameZh, badgeTierNameEn, badgeTierColor)
	if authorID.Valid {
		detail.AuthorUserID = authorID.Int64
		detail.Author = userSummaryWithAvatar(builder, authorID, username, displayName, email, avatarAttachmentID, attachmentID, attachmentPublicID, attachmentOwnerID, attachmentContentType, attachmentStatus)
	}
	return detail, nil
}
