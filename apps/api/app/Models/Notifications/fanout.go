package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/jackc/pgx/v5"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
)

const moderationReviewPermission = "moderation.review"

func coreDeliveryChannels() []string {
	return []string{"in_app", "email", "web_push"}
}

type CommentEvent struct {
	CommentID, TopicID, ActorUserID       int64
	TopicAuthorUserID, ParentAuthorUserID int64
	MentionedUsernames                    []string
}

type TopicEvent struct {
	TopicID, ActorUserID int64
	MentionedUsernames   []string
}

type ModerationEvent struct {
	DecisionID               int64
	TargetType               string
	TargetID, ReviewerUserID int64
	Approved                 bool
	ReviewNote               string
}

type PendingReviewEvent struct {
	TargetType, Title               string
	TargetID, TopicID, AuthorUserID int64
	Revision                        int64
}

func (o *Outbox) NotifyPendingReviewTx(ctx context.Context, tx pgx.Tx, event PendingReviewEvent) error {
	if o.recipients == nil {
		return nil
	}
	if event.TargetID <= 0 || event.TopicID <= 0 || event.AuthorUserID <= 0 || event.Revision <= 0 ||
		(event.TargetType != "topic" && event.TargetType != "comment") {
		return fmt.Errorf("invalid pending moderation notification event")
	}
	reviewerIDs, err := o.recipients.ListActiveUserIDsWithPermissionTx(ctx, tx, moderationReviewPermission)
	if err != nil {
		return fmt.Errorf("resolve moderation notification recipients: %w", err)
	}
	if event.Title == "" {
		if err := tx.QueryRow(ctx, `SELECT title FROM topics WHERE id=$1`, event.TopicID).Scan(&event.Title); err != nil {
			return fmt.Errorf("load pending moderation topic title: %w", err)
		}
	}
	brand := resolveMailBrand(ctx, o.brandResolver)
	reviewPath := "/moderation?" + url.Values{
		"reviewId":   {fmt.Sprintf("%d", event.TargetID)},
		"reviewType": {event.TargetType},
		"source":     {"pre_publish"},
	}.Encode()
	for _, reviewerID := range reviewerIDs {
		if reviewerID == event.AuthorUserID {
			continue
		}
		var email, locale, displayName string
		if err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, reviewerID).Scan(&email, &locale, &displayName); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return fmt.Errorf("load moderation reviewer recipient: %w", err)
		}
		payload, _ := json.Marshal(mailTemplateData(map[string]any{
			"targetType": event.TargetType, "targetId": event.TargetID, "topicId": event.TopicID,
			"revision": event.Revision, "title": event.Title, "reviewPath": reviewPath,
			"locale": resolveMailLocale(ctx, locale, o.localeResolver), "recipientName": displayName,
		}, brand))
		key := fmt.Sprintf("moderation-pending:%s:%d:%d:%d", event.TargetType, event.TargetID, event.Revision, reviewerID)
		if err := o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
			Notification: CreateInput{RecipientUserID: reviewerID, Type: TypeModerationPending, TargetType: "moderation_" + event.TargetType, TargetID: event.TargetID, Payload: payload, DedupeKey: key},
			Delivery:     CreateDeliveryInput{Recipient: email, TemplateKey: "forum." + TypeModerationPending, TemplateData: payload, IdempotencyKey: key},
			Channels:     coreDeliveryChannels(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func (o *Outbox) NotifyModerationTx(ctx context.Context, tx pgx.Tx, event ModerationEvent) error {
	brand := resolveMailBrand(ctx, o.brandResolver)
	var authorID sql.NullInt64
	topicID := event.TargetID
	var err error
	switch event.TargetType {
	case "topic":
		err = tx.QueryRow(ctx, `SELECT author_user_id FROM topics WHERE id=$1`, event.TargetID).Scan(&authorID)
	case "comment":
		err = tx.QueryRow(ctx, `SELECT author_user_id, topic_id FROM comments WHERE id=$1`, event.TargetID).Scan(&authorID, &topicID)
	default:
		return fmt.Errorf("unsupported moderation target type %q", event.TargetType)
	}
	if err != nil {
		return fmt.Errorf("load moderation notification target: %w", err)
	}
	kind := TypeModerationRejected
	if event.Approved {
		kind = TypeModerationApproved
	}
	if authorID.Valid && authorID.Int64 != event.ReviewerUserID {
		var email, locale, displayName string
		err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, authorID.Int64).Scan(&email, &locale, &displayName)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load moderation notification recipient: %w", err)
		}
		if err == nil {
			payloadData := mailTemplateData(map[string]any{"targetType": event.TargetType, "targetId": event.TargetID, "topicId": topicID, "reviewNote": event.ReviewNote, "locale": resolveMailLocale(ctx, locale, o.localeResolver), "recipientName": displayName}, brand)
			if event.TargetType == "comment" {
				payloadData["commentId"] = event.TargetID
			}
			payload, _ := json.Marshal(payloadData)
			key := fmt.Sprintf("moderation:%d:%s:%d", event.DecisionID, kind, authorID.Int64)
			if err := o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
				Notification: CreateInput{RecipientUserID: authorID.Int64, Type: kind, ActorUserID: &event.ReviewerUserID, TargetType: event.TargetType, TargetID: event.TargetID, Payload: payload, DedupeKey: key},
				Delivery:     CreateDeliveryInput{Recipient: email, TemplateKey: "forum." + kind, TemplateData: payload, IdempotencyKey: key},
				Channels:     coreDeliveryChannels(),
			}); err != nil {
				return err
			}
		}
	}
	if event.Approved {
		return o.notifyApprovedContentTx(ctx, tx, event)
	}
	return nil
}

// notifyApprovedContentTx deliberately reloads the persisted source inside the
// moderation transaction. Request input is no longer authoritative after
// filters and review have run.
func (o *Outbox) notifyApprovedContentTx(ctx context.Context, tx pgx.Tx, event ModerationEvent) error {
	if event.TargetType == "topic" {
		var authorID sql.NullInt64
		var rawContent, sourceFormat string
		if err := tx.QueryRow(ctx, `SELECT topics.author_user_id, posts.raw_content, posts.source_format FROM topics JOIN posts ON posts.id=topics.content_id WHERE topics.id=$1`, event.TargetID).Scan(&authorID, &rawContent, &sourceFormat); err != nil {
			return err
		}
		if !authorID.Valid {
			return nil
		}
		return o.NotifyTopicTx(ctx, tx, TopicEvent{TopicID: event.TargetID, ActorUserID: authorID.Int64, MentionedUsernames: forum.MentionedUsernames(forum.PublicMentionSource(rawContent, sourceFormat))})
	}
	if event.TargetType != "comment" {
		return nil
	}
	var actorID sql.NullInt64
	var topicID, topicAuthorID, parentAuthorID int64
	var rawContent, sourceFormat string
	if err := tx.QueryRow(ctx, `
		SELECT comments.author_user_id, comments.topic_id, COALESCE(topics.author_user_id, 0),
			COALESCE(parent.author_user_id, 0), posts.raw_content, posts.source_format
		FROM comments
		JOIN topics ON topics.id=comments.topic_id
		JOIN posts ON posts.id=comments.content_id
		LEFT JOIN comments parent ON parent.id=comments.parent_comment_id
		WHERE comments.id=$1`, event.TargetID).Scan(&actorID, &topicID, &topicAuthorID, &parentAuthorID, &rawContent, &sourceFormat); err != nil {
		return err
	}
	if !actorID.Valid {
		return nil
	}
	return o.NotifyCommentTx(ctx, tx, CommentEvent{CommentID: event.TargetID, TopicID: topicID, ActorUserID: actorID.Int64, TopicAuthorUserID: topicAuthorID, ParentAuthorUserID: parentAuthorID, MentionedUsernames: forum.MentionedUsernames(forum.PublicMentionSource(rawContent, sourceFormat))})
}

func (o *Outbox) NotifyCommentTx(ctx context.Context, tx pgx.Tx, event CommentEvent) error {
	brand := resolveMailBrand(ctx, o.brandResolver)
	type recipient struct {
		id                        int64
		email, kind, locale, name string
	}
	recipients := map[string]recipient{}
	replyRecipientID := event.ParentAuthorUserID
	if replyRecipientID == 0 {
		replyRecipientID = event.TopicAuthorUserID
	}
	if replyRecipientID > 0 && replyRecipientID != event.ActorUserID {
		var email, locale, name string
		err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, replyRecipientID).Scan(&email, &locale, &name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load reply notification recipient: %w", err)
		}
		if err == nil {
			recipients[fmt.Sprintf("reply:%d", replyRecipientID)] = recipient{id: replyRecipientID, email: email, kind: TypeReply, locale: locale, name: name}
		}
	}
	for _, username := range event.MentionedUsernames {
		var id int64
		var email, locale, name string
		err := tx.QueryRow(ctx, `SELECT id, email, locale, display_name FROM users WHERE username_lower=LOWER($1) AND status='active'`, username).Scan(&id, &email, &locale, &name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load mention notification recipient: %w", err)
		}
		if err != nil || id == event.ActorUserID {
			continue
		}
		recipients[fmt.Sprintf("mention:%d", id)] = recipient{id: id, email: email, kind: TypeMention, locale: locale, name: name}
	}
	// 当 mention 接收者同时是 reply 接收者时，合并为一条 reply 通知，
	// 通过 alsoMentioned 标记保留「被提及」语义，避免同一用户收到两条通知。
	mergedIntoReply := map[int64]bool{}
	for key, item := range recipients {
		if item.kind == TypeMention {
			if _, isReplyRecipient := recipients[fmt.Sprintf("reply:%d", item.id)]; isReplyRecipient {
				mergedIntoReply[item.id] = true
				delete(recipients, key)
			}
		}
	}
	for key, item := range recipients {
		payloadData := map[string]any{"commentId": event.CommentID, "topicId": event.TopicID, "locale": resolveMailLocale(ctx, item.locale, o.localeResolver), "recipientName": item.name}
		if item.kind == TypeReply && mergedIntoReply[item.id] {
			// 邮件渲染层将 TemplateData 解析为 map[string]string，这里用字符串标记。
			payloadData["alsoMentioned"] = "true"
		}
		payload, _ := json.Marshal(mailTemplateData(payloadData, brand))
		dedupe := fmt.Sprintf("comment:%d:%s", event.CommentID, key)
		if err := o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
			Notification: CreateInput{RecipientUserID: item.id, Type: item.kind, ActorUserID: &event.ActorUserID, TargetType: "comment", TargetID: event.CommentID, Payload: payload, DedupeKey: dedupe},
			Delivery:     CreateDeliveryInput{Recipient: item.email, TemplateKey: "forum." + item.kind, TemplateData: payload, IdempotencyKey: dedupe},
			Channels:     coreDeliveryChannels(),
		}); err != nil {
			return err
		}
	}
	return nil
}

// NotifyTopicTx mirrors comment mention fanout for an active topic creation.
// Topic authors are not notified about their own new topic; only explicit active
// mentions become projections.
func (o *Outbox) NotifyTopicTx(ctx context.Context, tx pgx.Tx, event TopicEvent) error {
	brand := resolveMailBrand(ctx, o.brandResolver)
	type recipient struct{ email, locale, name string }
	recipients := make(map[int64]recipient, len(event.MentionedUsernames))
	for _, username := range event.MentionedUsernames {
		var recipientID int64
		var email, locale, name string
		err := tx.QueryRow(ctx, `SELECT id, email, locale, display_name FROM users WHERE username_lower=LOWER($1) AND status='active'`, username).Scan(&recipientID, &email, &locale, &name)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("load topic mention notification recipient: %w", err)
		}
		if err != nil || recipientID == event.ActorUserID {
			continue
		}
		recipients[recipientID] = recipient{email: email, locale: locale, name: name}
	}
	for recipientID, recipient := range recipients {
		payload, _ := json.Marshal(mailTemplateData(map[string]any{"topicId": event.TopicID, "locale": resolveMailLocale(ctx, recipient.locale, o.localeResolver), "recipientName": recipient.name}, brand))
		key := fmt.Sprintf("topic:%d:mention:%d", event.TopicID, recipientID)
		if err := o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
			Notification: CreateInput{RecipientUserID: recipientID, Type: TypeMention, ActorUserID: &event.ActorUserID, TargetType: "topic", TargetID: event.TopicID, Payload: payload, DedupeKey: key},
			Delivery:     CreateDeliveryInput{Recipient: recipient.email, TemplateKey: "forum." + TypeMention, TemplateData: payload, IdempotencyKey: key},
			Channels:     coreDeliveryChannels(),
		}); err != nil {
			return err
		}
	}
	return nil
}

func mailTemplateData(data map[string]any, brand MailBrand) map[string]any {
	for name, value := range brand.TemplateData() {
		data[name] = value
	}
	return data
}
