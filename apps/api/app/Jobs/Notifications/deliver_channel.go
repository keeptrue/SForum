package notificationjobs

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"

	"github.com/riverqueue/river"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

const (
	WebPushProviderSlot     = "notification.channel.web_push"
	WebPushProviderContract = "sforum.web-push.channel@1"
	WebPushProviderInput    = "sforum.web-push.channel.request@1"
)

type DeliverChannelArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (DeliverChannelArgs) Kind() string { return "notification.channel.deliver" }
func (DeliverChannelArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{Queue: supportjobs.QueueMail, MaxAttempts: 5, Unique: river.UniqueOpts{ByArgs: true}}
}

type ChannelDeliveryStore interface {
	GetChannelDelivery(context.Context, int64) (notifications.ChannelDelivery, error)
	UpdateChannelDelivery(context.Context, notifications.ChannelDeliveryUpdate) error
	ListWebPushSubscriptions(context.Context, int64, bool) ([]notifications.WebPushSubscription, error)
	GetWebPushDeliveryAttempt(context.Context, int64, int64) (notifications.WebPushDeliveryAttempt, error)
	UpdateWebPushDeliveryAttempt(context.Context, notifications.WebPushDeliveryAttempt) error
	MarkWebPushSubscriptionFailed(context.Context, int64, string) error
}

type ChannelProviderInvoker interface {
	InvokeNotificationChannel(context.Context, ChannelProviderRequest) (ChannelProviderResult, error)
}

var ErrChannelProviderUnavailable = errors.New("notification channel provider is unavailable")

type ChannelProviderRequest struct {
	SlotID, ContractVersion, InputSchema string
	Input                                map[string]any
}

type ChannelProviderResult struct {
	ExtensionID, ArtifactDigest string
	Output                      map[string]any
}

type DeliverChannelWorker struct {
	river.WorkerDefaults[DeliverChannelArgs]
	Store   ChannelDeliveryStore
	Invoker ChannelProviderInvoker
}

func (w *DeliverChannelWorker) Work(ctx context.Context, job *river.Job[DeliverChannelArgs]) error {
	if w.Store == nil || w.Invoker == nil {
		return errors.New("notification channel worker is not configured")
	}
	delivery, err := w.Store.GetChannelDelivery(ctx, job.Args.DeliveryID)
	if err != nil {
		return err
	}
	if outbox.IsTerminal(delivery.Status) {
		return nil
	}
	if delivery.Channel != "web_push" {
		return w.finish(ctx, delivery, notifications.DeliveryFailed, "notification.channel_unsupported", "")
	}
	subscriptions, err := w.Store.ListWebPushSubscriptions(ctx, delivery.RecipientUserID, true)
	if err != nil {
		return err
	}
	if len(subscriptions) == 0 {
		return w.finish(ctx, delivery, notifications.DeliverySkipped, "notification.subscription_unavailable", "")
	}
	attemptNumber := delivery.AttemptCount + 1
	if err := w.Store.UpdateChannelDelivery(ctx, notifications.ChannelDeliveryUpdate{
		ID: delivery.ID, Status: notifications.DeliverySending, AttemptCount: attemptNumber,
	}); err != nil {
		return err
	}

	var temporary bool
	var provider ChannelProviderResult
	for _, subscription := range subscriptions {
		attempt, err := w.Store.GetWebPushDeliveryAttempt(ctx, delivery.ID, subscription.ID)
		if err != nil {
			return err
		}
		if attempt.Status == notifications.DeliverySent {
			continue
		}
		attempt.AttemptCount++
		provider, err = w.sendWebPush(ctx, delivery, subscription)
		if err != nil {
			attempt.Status, attempt.Reason = notifications.DeliveryQueued, "notification.provider_unavailable"
			_ = w.Store.UpdateWebPushDeliveryAttempt(ctx, attempt)
			if errors.Is(err, ErrChannelProviderUnavailable) {
				return w.finish(ctx, delivery, notifications.DeliverySkipped, "notification.provider_unavailable", "")
			}
			temporary = true
			continue
		}
		ok, classification, reason, message := channelProviderResult(provider.Output)
		attempt.Reason = reason
		switch {
		case ok:
			attempt.Status = notifications.DeliverySent
		case classification == "temporary":
			attempt.Status = notifications.DeliveryQueued
			temporary = true
		default:
			attempt.Status = notifications.DeliveryFailed
			if reason == "web_push.subscription_expired" || reason == "web_push.subscription_invalid" {
				_ = w.Store.MarkWebPushSubscriptionFailed(ctx, subscription.ID, reason)
			}
		}
		if err := w.Store.UpdateWebPushDeliveryAttempt(ctx, attempt); err != nil {
			return err
		}
		if message != "" && !ok && classification == "temporary" {
			_ = message // Provider text is deliberately never persisted or logged.
		}
	}
	update := notifications.ChannelDeliveryUpdate{
		ID: delivery.ID, AttemptCount: attemptNumber, ProviderExtensionID: provider.ExtensionID,
		ProviderArtifactDigest: provider.ArtifactDigest,
	}
	if temporary {
		update.Status, update.Reason = notifications.DeliverySending, "notification.provider_temporary"
		_ = w.Store.UpdateChannelDelivery(ctx, update)
		return errors.New("temporary Web Push provider failure")
	}
	update.Status, update.Reason = notifications.DeliverySent, "web_push.sent"
	return w.Store.UpdateChannelDelivery(ctx, update)
}

func (w *DeliverChannelWorker) sendWebPush(ctx context.Context, delivery notifications.ChannelDelivery, subscription notifications.WebPushSubscription) (ChannelProviderResult, error) {
	return w.Invoker.InvokeNotificationChannel(ctx, ChannelProviderRequest{
		SlotID: WebPushProviderSlot, ContractVersion: WebPushProviderContract,
		InputSchema: WebPushProviderInput,
		Input: map[string]any{
			"operation":  "send",
			"deliveryId": strconv.FormatInt(delivery.ID, 10),
			"subscription": map[string]any{
				"endpoint": subscription.Endpoint,
				"p256dh":   base64.RawURLEncoding.EncodeToString(subscription.P256DHKey),
				"auth":     base64.RawURLEncoding.EncodeToString(subscription.AuthKey),
			},
			"notification": map[string]any{
				"title": "SForum", "body": "You have a new notification.",
				"url": "/notifications", "tag": "notification-" + strconv.FormatInt(delivery.ID, 10),
			},
		},
	})
}

func channelProviderResult(output map[string]any) (ok bool, classification, reason, message string) {
	ok, _ = output["ok"].(bool)
	classification, _ = output["classification"].(string)
	reason, _ = output["reason"].(string)
	message, _ = output["message"].(string)
	return
}

func (w *DeliverChannelWorker) finish(ctx context.Context, delivery notifications.ChannelDelivery, status, reason, summary string) error {
	if len(summary) > 160 {
		summary = summary[:160]
	}
	if err := w.Store.UpdateChannelDelivery(ctx, notifications.ChannelDeliveryUpdate{
		ID: delivery.ID, Status: status, AttemptCount: delivery.AttemptCount + 1,
		Reason: reason, ErrorSummary: summary,
	}); err != nil {
		return fmt.Errorf("finish notification channel delivery: %w", err)
	}
	return nil
}

func RegisterChannels(registry *supportjobs.Registry, worker *DeliverChannelWorker) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[DeliverChannelArgs](workers, worker)
	})
}
