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

// ReactionEvent 是一次点赞的收件人视角：目标作者收到「X 赞了你的内容」。
// RecipientUserID 为 0 或等于 ActorUserID 时不写投影（自赞不打扰）。
// BookmarkReminderEvent 是一次到期的收藏提醒。
//
// 与 reaction 的差别：收件人就是收藏行主（没有 actor），去重键锚在收藏行 ID 上，
// 因此同一行只会产生一条提醒投影，即使 job 被重试。
type BookmarkReminderEvent struct {
	BookmarkID      int64
	RecipientUserID int64
	TargetType      string
	TargetID        int64
	TopicID         int64
}

type ReactionEvent struct {
	TargetType      string
	TargetID        int64
	TopicID         int64
	ActorUserID     int64
	RecipientUserID int64
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

// NotifyReactionTx writes the like notification projection for the target author.
//
// 与 reply/mention 同一套投影与偏好规则：in_app 默认开，email/web_push 默认关。
// 去重键包含 actor，因此「取消后再赞」不会产生第二条通知。
func (o *Outbox) NotifyReactionTx(ctx context.Context, tx pgx.Tx, event ReactionEvent) error {
	if event.RecipientUserID <= 0 || event.RecipientUserID == event.ActorUserID {
		return nil
	}
	brand := resolveMailBrand(ctx, o.brandResolver)
	var email, locale, name string
	err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, event.RecipientUserID).Scan(&email, &locale, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load reaction notification recipient: %w", err)
	}
	payload, _ := json.Marshal(mailTemplateData(map[string]any{
		"topicId":       event.TopicID,
		"targetType":    event.TargetType,
		"targetId":      event.TargetID,
		"locale":        resolveMailLocale(ctx, locale, o.localeResolver),
		"recipientName": name,
	}, brand))
	dedupe := fmt.Sprintf("reaction:%s:%d:%d", event.TargetType, event.TargetID, event.ActorUserID)
	return o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
		Notification: CreateInput{
			RecipientUserID: event.RecipientUserID,
			Type:            TypeReaction,
			ActorUserID:     &event.ActorUserID,
			TargetType:      event.TargetType,
			TargetID:        event.TargetID,
			Payload:         payload,
			DedupeKey:       dedupe,
		},
		Delivery: CreateDeliveryInput{Recipient: email, TemplateKey: "forum." + TypeReaction, TemplateData: payload, IdempotencyKey: dedupe},
		Channels: coreDeliveryChannels(),
	})
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

// NotifyBookmarkReminderTx writes the reminder projection for a claimed bookmark.
//
// 调用方（Forum 的到期扫描）已经在同一事务里把 reminded_at 写掉，所以这里失败
// 会让整条提醒回滚、下一轮重扫——不会出现「标了已提醒但没发」。
func (o *Outbox) NotifyBookmarkReminderTx(ctx context.Context, tx pgx.Tx, event BookmarkReminderEvent) error {
	if event.RecipientUserID <= 0 || event.BookmarkID <= 0 {
		return nil
	}
	brand := resolveMailBrand(ctx, o.brandResolver)
	var email, locale, name string
	err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, event.RecipientUserID).Scan(&email, &locale, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load bookmark reminder recipient: %w", err)
	}
	payload, _ := json.Marshal(mailTemplateData(map[string]any{
		"bookmarkId":    event.BookmarkID,
		"topicId":       event.TopicID,
		"targetType":    event.TargetType,
		"targetId":      event.TargetID,
		"locale":        resolveMailLocale(ctx, locale, o.localeResolver),
		"recipientName": name,
	}, brand))
	dedupe := fmt.Sprintf("bookmark_reminder:%d", event.BookmarkID)
	return o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
		Notification: CreateInput{
			RecipientUserID: event.RecipientUserID,
			Type:            TypeBookmarkReminder,
			TargetType:      event.TargetType,
			TargetID:        event.TargetID,
			Payload:         payload,
			DedupeKey:       dedupe,
		},
		Delivery: CreateDeliveryInput{Recipient: email, TemplateKey: "forum." + TypeBookmarkReminder, TemplateData: payload, IdempotencyKey: dedupe},
		Channels: coreDeliveryChannels(),
	})
}

func mailTemplateData(data map[string]any, brand MailBrand) map[string]any {
	for name, value := range brand.TemplateData() {
		data[name] = value
	}
	return data
}

// BadgeGrantedEvent 是一次徽章授予（自动或手动）。
//
// 刻意没有「自己给自己」的抑制：徽章是公开荣誉，不是互动噪音。
// 与 ReactionEvent 的语义相反——那边自赞要静默，这边自授要通知。
type BadgeGrantedEvent struct {
	RecipientUserID int64
	BadgeSlug       string
	BadgeName       string
	// GrantedByUserID 为 0 表示系统自动授予（规则命中）。
	GrantedByUserID int64
}

// NotifyBadgeGranted 在 job 里调用（没有现成事务）：自己开一个。
func (o *Outbox) NotifyBadgeGranted(ctx context.Context, event BadgeGrantedEvent) error {
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin badge notification: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()
	if err := o.NotifyBadgeGrantedTx(ctx, tx, event); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit badge notification: %w", err)
	}
	return nil
}

// NotifyBadgeGrantedTx 写一条徽章授予投影。
//
// 幂等靠 dedupeKey = badge:{slug}：同一用户同一徽章只会有一条通知，
// 重复评估不会刷屏。授予被收回后再授予也不会重复（徽章是终局荣誉）。
func (o *Outbox) NotifyBadgeGrantedTx(ctx context.Context, tx pgx.Tx, event BadgeGrantedEvent) error {
	if event.RecipientUserID <= 0 || event.BadgeSlug == "" {
		return nil
	}
	brand := resolveMailBrand(ctx, o.brandResolver)
	var email, locale, name string
	err := tx.QueryRow(ctx, `SELECT email, locale, display_name FROM users WHERE id=$1 AND status='active'`, event.RecipientUserID).Scan(&email, &locale, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		// 用户不存在或已停用：不写通知（与 reaction 同一姿态）。
		return nil
	}
	if err != nil {
		return fmt.Errorf("load badge notification recipient: %w", err)
	}
	payload, _ := json.Marshal(mailTemplateData(map[string]any{
		"badgeSlug":     event.BadgeSlug,
		"badgeName":     event.BadgeName,
		"locale":        resolveMailLocale(ctx, locale, o.localeResolver),
		"recipientName": name,
	}, brand))
	// dedupe 只看 slug：同一用户同一徽章终身只通知一次；手动收回后重新授予不再通知
	// （有意为之，徽章是终局荣誉）。改成按授予事件去重会牵动 GrantAutomatic 的签名，
	// 而那会把「授予是否新建」与「通知去重键」耦合在一起。
	dedupe := fmt.Sprintf("badge:%s", event.BadgeSlug)
	var actor *int64
	if event.GrantedByUserID > 0 {
		value := event.GrantedByUserID
		actor = &value
	}
	return o.CreateProjectionsTx(ctx, tx, CreateBundleInput{
		Notification: CreateInput{
			RecipientUserID: event.RecipientUserID,
			Type:            TypeBadgeGranted,
			ActorUserID:     actor,
			Payload:         payload,
			DedupeKey:       dedupe,
		},
		Delivery: CreateDeliveryInput{Recipient: email, TemplateKey: "forum." + TypeBadgeGranted, TemplateData: payload, IdempotencyKey: dedupe},
		Channels: coreDeliveryChannels(),
	})
}
