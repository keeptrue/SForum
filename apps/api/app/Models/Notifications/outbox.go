package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type TxEnqueuer interface {
	EnqueueTx(context.Context, pgx.Tx, river.JobArgs, supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error)
}

type PermissionRecipientResolver interface {
	ListActiveUserIDsWithPermissionTx(context.Context, pgx.Tx, string) ([]int64, error)
}

type Outbox struct {
	pool interface {
		Begin(context.Context) (pgx.Tx, error)
	}
	store          *PostgresStore
	jobs           TxEnqueuer
	localeResolver MailLocaleResolver
	brandResolver  MailBrandResolver
	recipients     PermissionRecipientResolver
}

func (o *Outbox) WithPermissionRecipients(resolver PermissionRecipientResolver) *Outbox {
	o.recipients = resolver
	return o
}

func NewOutbox(pool interface {
	Begin(context.Context) (pgx.Tx, error)
}, store *PostgresStore, jobs TxEnqueuer, localeResolvers ...MailLocaleResolver) *Outbox {
	var localeResolver MailLocaleResolver
	if len(localeResolvers) > 0 {
		localeResolver = localeResolvers[0]
	}
	brandResolver, _ := localeResolver.(MailBrandResolver)
	return &Outbox{pool: pool, store: store, jobs: jobs, localeResolver: localeResolver, brandResolver: brandResolver}
}

func (o *Outbox) WithDeliveryPolicyResolver(resolver DeliveryPolicyResolver) *Outbox {
	// Kept as a compatibility builder while all transactional projection paths
	// resolve through the store on the caller's transaction snapshot.
	_ = resolver
	return o
}

type QueueMailInput struct {
	Recipient, TemplateKey, IdempotencyKey, CorrelationID string
	TemplateData                                          json.RawMessage
}
type QueuePasswordResetInput struct {
	UserID        int64
	TokenHash     string
	ExpiresAt     time.Time
	RequestIPHash string
	Mail          QueueMailInput
}
type QueueEmailVerificationInput struct {
	UserID        int64
	Email         string
	TokenHash     string
	ExpiresAt     time.Time
	RequestIPHash string
	Mail          QueueMailInput
}

type deliverMailArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (deliverMailArgs) Kind() string { return "mail.deliver" }
func (deliverMailArgs) enqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{Queue: supportjobs.QueueMail, MaxAttempts: 5, Unique: river.UniqueOpts{ByArgs: true}}
}

type deliverChannelArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (deliverChannelArgs) Kind() string { return "notification.channel.deliver" }
func (deliverChannelArgs) enqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{Queue: supportjobs.QueueMail, MaxAttempts: 5, Unique: river.UniqueOpts{ByArgs: true}}
}

func (o *Outbox) QueueMail(ctx context.Context, input QueueMailInput) (MailDelivery, error) {
	return o.inTx(ctx, input, nil)
}

func (o *Outbox) QueuePasswordReset(ctx context.Context, input QueuePasswordResetInput) (MailDelivery, error) {
	return o.inTx(ctx, input.Mail, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO password_reset_tokens (user_id, token_hash, expires_at, request_ip_hash) VALUES ($1,$2,$3,$4)`, input.UserID, input.TokenHash, input.ExpiresAt, input.RequestIPHash)
		return err
	})
}

func (o *Outbox) QueueEmailVerification(ctx context.Context, input QueueEmailVerificationInput) (MailDelivery, error) {
	return o.inTx(ctx, input.Mail, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			UPDATE email_verification_tokens SET consumed_at = now()
			WHERE user_id = $1 AND consumed_at IS NULL
		`, input.UserID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO email_verification_tokens (user_id, email, token_hash, expires_at, request_ip_hash)
			VALUES ($1, $2, $3, $4, $5)
		`, input.UserID, input.Email, input.TokenHash, input.ExpiresAt, input.RequestIPHash)
		return err
	})
}

func (o *Outbox) QueueChannel(ctx context.Context, input CreateInput, channel string) (ChannelDelivery, error) {
	if o == nil || o.pool == nil || o.store == nil || o.jobs == nil || channel != "web_push" {
		return ChannelDelivery{}, fmt.Errorf("notifications: channel outbox is not configured")
	}
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return ChannelDelivery{}, err
	}
	defer tx.Rollback(ctx)
	payloadVersion := input.PayloadVersion
	if payloadVersion <= 0 {
		payloadVersion = 1
	}
	delivery, err := o.store.CreateChannelDeliveryTx(ctx, tx, CreateChannelDeliveryInput{
		RecipientUserID: input.RecipientUserID, Type: input.Type, Channel: channel,
		PayloadVersion: payloadVersion, Payload: input.Payload, TargetMeta: webPushTargetMeta(input),
		IdempotencyKey: channelProjectionKey(input.DedupeKey, channel),
	})
	if err != nil {
		return ChannelDelivery{}, err
	}
	args := deliverChannelArgs{DeliveryID: delivery.ID}
	if _, err := o.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
		return ChannelDelivery{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ChannelDelivery{}, err
	}
	return delivery, nil
}

func (o *Outbox) inTx(ctx context.Context, input QueueMailInput, before func(pgx.Tx) error) (MailDelivery, error) {
	if o == nil || o.pool == nil || o.store == nil || o.jobs == nil {
		return MailDelivery{}, fmt.Errorf("notifications: outbox is not configured")
	}
	tx, err := o.pool.Begin(ctx)
	if err != nil {
		return MailDelivery{}, err
	}
	defer tx.Rollback(ctx)
	if before != nil {
		if err := before(tx); err != nil {
			return MailDelivery{}, err
		}
	}
	delivery, err := o.store.CreateDeliveryTx(ctx, tx, CreateDeliveryInput{Recipient: input.Recipient, TemplateKey: input.TemplateKey, TemplateData: input.TemplateData, IdempotencyKey: input.IdempotencyKey, CorrelationID: input.CorrelationID})
	if err != nil {
		return MailDelivery{}, err
	}
	args := deliverMailArgs{DeliveryID: delivery.ID}
	if _, err = o.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
		return MailDelivery{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return MailDelivery{}, err
	}
	return delivery, nil
}

func (o *Outbox) CreateBundleTx(ctx context.Context, tx pgx.Tx, input CreateBundleInput) (Bundle, error) {
	bundle, err := o.store.CreateBundleTx(ctx, tx, input)
	if err != nil {
		return Bundle{}, err
	}
	args := deliverMailArgs{DeliveryID: bundle.Delivery.ID}
	if _, err = o.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

// CreateProjectionsTx is the shared policy and channel projection boundary for
// Core fanout and exact-artifact Host commands. The caller owns the transaction.
type ProjectionResult struct {
	InApp, Email, WebPush bool
}

func (o *Outbox) CreateProjectionsTx(ctx context.Context, tx pgx.Tx, input CreateBundleInput) error {
	_, err := o.CreateProjectionsResultTx(ctx, tx, input)
	return err
}

func (o *Outbox) CreateProjectionsResultTx(ctx context.Context, tx pgx.Tx, input CreateBundleInput) (ProjectionResult, error) {
	if o == nil || o.store == nil || o.jobs == nil || tx == nil {
		return ProjectionResult{}, fmt.Errorf("notifications: projection outbox is not configured")
	}
	channels := input.Channels
	if len(channels) == 0 {
		channels = []string{"in_app", "email"}
	}
	result := ProjectionResult{}
	for _, channel := range channels {
		enabled, err := o.store.DeliveryEnabledTx(ctx, tx, input.Notification.RecipientUserID, input.Notification.Type, channel)
		if err != nil {
			return ProjectionResult{}, err
		}
		switch channel {
		case "in_app":
			result.InApp = enabled
		case "email":
			result.Email = enabled
		case "web_push":
			result.WebPush = enabled
		}
	}
	return o.writeProjectionsTx(ctx, tx, input, result)
}

// createProjectionsTx preserves the Core compatibility policy inputs while
// adding the V2 Web Push projection from the same transaction snapshot.
func (o *Outbox) createProjectionsTx(ctx context.Context, tx pgx.Tx, input CreateBundleInput, inApp, email bool) error {
	if o == nil || o.store == nil || o.jobs == nil || tx == nil {
		return fmt.Errorf("notifications: projection outbox is not configured")
	}
	webPush, err := o.store.DeliveryEnabledTx(ctx, tx, input.Notification.RecipientUserID, input.Notification.Type, "web_push")
	if err != nil {
		return err
	}
	_, err = o.writeProjectionsTx(ctx, tx, input, ProjectionResult{InApp: inApp, Email: email, WebPush: webPush})
	return err
}

func (o *Outbox) writeProjectionsTx(ctx context.Context, tx pgx.Tx, input CreateBundleInput, result ProjectionResult) (ProjectionResult, error) {
	var notificationID *int64
	if result.InApp {
		item, err := o.store.CreateNotificationTx(ctx, tx, input.Notification)
		if err != nil {
			return ProjectionResult{}, err
		}
		notificationID = &item.ID
	}
	if result.Email {
		delivery, err := o.store.CreateDeliveryTx(ctx, tx, input.Delivery)
		if err != nil {
			return ProjectionResult{}, err
		}
		args := deliverMailArgs{DeliveryID: delivery.ID}
		if _, err = o.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
			return ProjectionResult{}, err
		}
	}
	if result.WebPush {
		payloadVersion := input.Notification.PayloadVersion
		if payloadVersion <= 0 {
			payloadVersion = 1
		}
		delivery, err := o.store.CreateChannelDeliveryTx(ctx, tx, CreateChannelDeliveryInput{
			NotificationID: notificationID, RecipientUserID: input.Notification.RecipientUserID,
			Type: input.Notification.Type, Channel: "web_push", PayloadVersion: payloadVersion,
			Payload: input.Notification.Payload, TargetMeta: webPushTargetMeta(input.Notification),
			IdempotencyKey: channelProjectionKey(input.Notification.DedupeKey, "web_push"),
		})
		if err != nil {
			return ProjectionResult{}, err
		}
		args := deliverChannelArgs{DeliveryID: delivery.ID}
		if _, err = o.jobs.EnqueueTx(ctx, tx, args, args.enqueueOptions()); err != nil {
			return ProjectionResult{}, err
		}
	}
	return result, nil
}
