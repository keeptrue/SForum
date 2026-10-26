package notifications

import (
	"encoding/json"
	"errors"
	"time"

	avatar "github.com/keeptrue/sforum/apps/api/app/Support/Avatar"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

const (
	TypeReply              = "reply"
	TypeMention            = "mention"
	TypeModerationPending  = "moderation_pending"
	TypeModerationApproved = "moderation_approved"
	TypeModerationRejected = "moderation_rejected"
	TypeAdminTest          = "admin_test"

	// 邮件投递状态对齐 outbox 共享词表（F3.1）。
	DeliveryQueued  = outbox.StatusQueued
	DeliverySending = outbox.StatusSending
	DeliverySent    = outbox.StatusSent
	DeliveryFailed  = outbox.StatusFailed
	DeliverySkipped = outbox.StatusSkipped
)

var ErrNotificationNotFound = errors.New("notifications: notification not found")

type Notification struct {
	ID              int64              `json:"id"`
	RecipientUserID int64              `json:"-"`
	Type            string             `json:"type"`
	Category        string             `json:"category,omitempty"`
	TypeVersion     int                `json:"typeVersion,omitempty"`
	PayloadVersion  int                `json:"payloadVersion,omitempty"`
	ActorUserID     *int64             `json:"actorUserId,omitempty"`
	Actor           *NotificationActor `json:"actor,omitempty"`
	TargetType      string             `json:"targetType"`
	TargetID        int64              `json:"targetId"`
	TargetAvailable bool               `json:"targetAvailable"`
	TargetPath      string             `json:"targetPath,omitempty"`
	Payload         json.RawMessage    `json:"payload"`
	DedupeKey       string             `json:"-"`
	ReadAt          *time.Time         `json:"readAt,omitempty"`
	CreatedAt       time.Time          `json:"createdAt"`
}

type NotificationActor struct {
	ID          int64       `json:"id"`
	Username    string      `json:"username"`
	DisplayName string      `json:"displayName"`
	Avatar      avatar.View `json:"avatar"`
}

type TargetPreviewAuthor = NotificationActor

type TargetPreviewContent struct {
	Type    string               `json:"type"`
	ID      int64                `json:"id"`
	Excerpt string               `json:"excerpt"`
	Author  *TargetPreviewAuthor `json:"author,omitempty"`
}

type TargetPreview struct {
	TopicID    int64                 `json:"topicId"`
	TopicTitle string                `json:"topicTitle"`
	Content    TargetPreviewContent  `json:"content"`
	Context    *TargetPreviewContent `json:"context,omitempty"`
}

type NotificationDetail struct {
	Notification
	Preview *TargetPreview `json:"preview,omitempty"`
}

type MailDelivery struct {
	ID             int64           `json:"id"`
	Recipient      string          `json:"recipient"`
	TemplateKey    string          `json:"templateKey"`
	TemplateData   json.RawMessage `json:"templateData,omitempty"`
	IdempotencyKey string          `json:"-"`
	CorrelationID  string          `json:"correlationId"`
	Status         string          `json:"status"`
	ExtensionID    string          `json:"extensionId,omitempty"`
	AttemptCount   int             `json:"attemptCount"`
	Reason         string          `json:"reason,omitempty"`
	ErrorSummary   string          `json:"errorSummary,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	UpdatedAt      time.Time       `json:"updatedAt"`
	CompletedAt    *time.Time      `json:"completedAt,omitempty"`
}

type CreateInput struct {
	RecipientUserID int64
	Type            string
	Category        string
	TypeVersion     int
	PayloadVersion  int
	ActorUserID     *int64
	TargetType      string
	TargetID        int64
	Payload         json.RawMessage
	DedupeKey       string
}

func categoryForType(typ string) string {
	switch typ {
	case TypeReply:
		return "conversation"
	case TypeMention:
		return "mention"
	case TypeModerationPending, TypeModerationApproved, TypeModerationRejected:
		return "moderation"
	case TypeAdminTest:
		return "system"
	default:
		return "plugin_unknown"
	}
}

type CreateDeliveryInput struct {
	Recipient      string
	TemplateKey    string
	TemplateData   json.RawMessage
	IdempotencyKey string
	CorrelationID  string
}

type CreateBundleInput struct {
	Notification CreateInput
	Delivery     CreateDeliveryInput
	// Channels is the validated type declaration. Empty preserves the legacy
	// Core bundle contract (in_app + email).
	Channels []string
}

type Bundle struct {
	Notification Notification
	Delivery     MailDelivery
}

type ListInput struct {
	RecipientUserID int64
	Limit           int
	BeforeID        int64
	Category        string
	Type            string
	Unread          *bool
}

type Page struct {
	Items   []Notification `json:"items"`
	HasMore bool           `json:"hasMore"`
}

type DeliveryUpdate struct {
	ID           int64
	Status       string
	ExtensionID  string
	AttemptCount int
	Reason       string
	ErrorSummary string
}
