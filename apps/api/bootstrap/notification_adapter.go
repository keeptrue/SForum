package bootstrap

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	moderation "github.com/keeptrue/sforum/apps/api/app/Models/Moderation"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
)

type forumNotificationAdapter struct{ outbox *notifications.Outbox }

type notificationTargetPreviewAdapter struct{ store *forum.PostgresStore }

type notificationActorLoader interface {
	LoadActor(context.Context, int64) (identity.Actor, error)
}

// notificationBadgeTargetResolver 是徽章通知目标的窄读口（见
// `Models/Badges/notification_targets.go`）。
//
// 窄接口而不是直接吃 `*badges.PostgresStore`：装配层只需要「徽章 id → 收件人可
// 点击路径」这一条读，把整个 store 传进来会让目标解析随手多出「读规则」「写授予」
// 这类本不该出现在收件箱读路径上的能力。
type notificationBadgeTargetResolver interface {
	ResolveNotificationBadgeTarget(ctx context.Context, userID int64, badgeID int64) (bool, string, error)
}

type notificationTargetVisibilityAdapter struct {
	forum      *forum.PostgresStore
	identities notificationActorLoader
	badges     notificationBadgeTargetResolver
}

func (a notificationTargetVisibilityAdapter) ResolveNotificationTarget(ctx context.Context, userID int64, targetType string, targetID int64) (bool, string, error) {
	// 徽章目标不走 Forum：徽章不是论坛内容，`/u/{username}?tab=badges` 也由
	// Badges 域自己判定（持有 + 启用 + 等级启用）。
	if targetType == "badge" {
		if a.badges == nil {
			return false, "", nil
		}
		return a.badges.ResolveNotificationBadgeTarget(ctx, userID, targetID)
	}
	moderationType, isModeration := strings.CutPrefix(targetType, "moderation_")
	if !isModeration {
		return a.forum.ResolveNotificationTarget(ctx, userID, targetType, targetID)
	}
	if targetID <= 0 || (moderationType != "topic" && moderationType != "comment") {
		return false, "", nil
	}
	actor, err := a.identities.LoadActor(ctx, userID)
	if err != nil {
		return false, "", err
	}
	if !actor.Can(identity.PermissionModerationReview) {
		return false, "", nil
	}
	query := url.Values{"source": {moderation.SourcePrePublish}, "reviewType": {moderationType}, "reviewId": {strconv.FormatInt(targetID, 10)}}
	return true, "/moderation?" + query.Encode(), nil
}

func (a notificationTargetPreviewAdapter) ResolveNotificationTargetPreview(ctx context.Context, _ int64, targetType string, targetID int64) (notifications.TargetPreview, bool, error) {
	preview, available, err := a.store.ResolveNotificationTargetPreview(ctx, targetType, targetID)
	if err != nil || !available {
		return notifications.TargetPreview{}, available, err
	}
	convertAuthor := func(user *forum.UserSummary) *notifications.TargetPreviewAuthor {
		if user == nil {
			return nil
		}
		return &notifications.TargetPreviewAuthor{ID: user.ID, Username: user.Username, DisplayName: user.DisplayName, Avatar: user.Avatar}
	}
	convertContent := func(content forum.NotificationTargetPreviewContent) notifications.TargetPreviewContent {
		return notifications.TargetPreviewContent{Type: content.Type, ID: content.ID, Excerpt: content.Excerpt, Author: convertAuthor(content.Author)}
	}
	result := notifications.TargetPreview{TopicID: preview.TopicID, TopicTitle: preview.TopicTitle, Content: convertContent(preview.Content)}
	if preview.Context != nil {
		context := convertContent(*preview.Context)
		result.Context = &context
	}
	return result, true, nil
}

func (a forumNotificationAdapter) NotifyCommentTx(ctx context.Context, tx pgx.Tx, input forum.CommentNotificationInput) error {
	return a.outbox.NotifyCommentTx(ctx, tx, notifications.CommentEvent{CommentID: input.CommentID, TopicID: input.TopicID, ActorUserID: input.ActorUserID, TopicAuthorUserID: input.TopicAuthorUserID, ParentAuthorUserID: input.ParentAuthorUserID, MentionedUsernames: input.MentionedUsernames})
}

func (a forumNotificationAdapter) NotifyTopicTx(ctx context.Context, tx pgx.Tx, input forum.TopicNotificationInput) error {
	return a.outbox.NotifyTopicTx(ctx, tx, notifications.TopicEvent{TopicID: input.TopicID, ActorUserID: input.ActorUserID, MentionedUsernames: input.MentionedUsernames})
}

func (a forumNotificationAdapter) NotifyReactionTx(ctx context.Context, tx pgx.Tx, input forum.ReactionNotificationInput) error {
	return a.outbox.NotifyReactionTx(ctx, tx, notifications.ReactionEvent{TargetType: input.TargetType, TargetID: input.TargetID, TopicID: input.TopicID, ActorUserID: input.ActorUserID, RecipientUserID: input.RecipientUserID})
}

// NotifyBookmarkReminderTx 让同一个 adapter 兼作收藏提醒投影入口（可选接口，
// 不塞进 forum.NotificationWriter，避免为一个新能力改所有既有 fake 的签名）。
func (a forumNotificationAdapter) NotifyBookmarkReminderTx(ctx context.Context, tx pgx.Tx, input forum.BookmarkReminderNotificationInput) error {
	return a.outbox.NotifyBookmarkReminderTx(ctx, tx, notifications.BookmarkReminderEvent{
		BookmarkID:      input.BookmarkID,
		RecipientUserID: input.RecipientUserID,
		TargetType:      input.TargetType,
		TargetID:        input.TargetID,
		TopicID:         input.TopicID,
	})
}

func (a forumNotificationAdapter) NotifyPendingReviewTx(ctx context.Context, tx pgx.Tx, input forum.PendingReviewNotificationInput) error {
	return a.outbox.NotifyPendingReviewTx(ctx, tx, notifications.PendingReviewEvent{TargetType: input.TargetType, TargetID: input.TargetID, TopicID: input.TopicID, AuthorUserID: input.AuthorUserID, Revision: input.Revision, Title: input.Title})
}

type moderationNotificationAdapter struct{ outbox *notifications.Outbox }

func (a moderationNotificationAdapter) NotifyModerationTx(ctx context.Context, tx pgx.Tx, input moderation.DecisionNotificationInput) error {
	return a.outbox.NotifyModerationTx(ctx, tx, notifications.ModerationEvent{DecisionID: input.DecisionID, TargetType: input.TargetType, TargetID: input.TargetID, ReviewerUserID: input.ReviewerUserID, Approved: input.Approved, ReviewNote: input.ReviewNote})
}
