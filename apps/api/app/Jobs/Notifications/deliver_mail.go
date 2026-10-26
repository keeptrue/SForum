package notificationjobs

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

type DeliverMailArgs struct {
	DeliveryID int64 `json:"delivery_id" river:"unique"`
}

func (DeliverMailArgs) Kind() string { return "mail.deliver" }
func (DeliverMailArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{Queue: supportjobs.QueueMail, MaxAttempts: 5, Unique: river.UniqueOpts{ByArgs: true}}
}

type DeliveryStore interface {
	GetDelivery(context.Context, int64) (notifications.MailDelivery, error)
	UpdateDelivery(context.Context, notifications.DeliveryUpdate) error
}
type ProviderResolver interface {
	Resolve(context.Context) (extensionsruntime.MailProviderSelection, extensionsruntime.MailProviderState, error)
}
type ProviderSender interface {
	SendMail(context.Context, string, extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error)
}

type DeliverMailWorker struct {
	river.WorkerDefaults[DeliverMailArgs]
	Store     DeliveryStore
	Providers ProviderResolver
	Sender    ProviderSender
}

func (w *DeliverMailWorker) Work(ctx context.Context, job *river.Job[DeliverMailArgs]) error {
	if w.Store == nil || w.Providers == nil {
		return fmt.Errorf("mail delivery worker is not configured")
	}
	delivery, err := w.Store.GetDelivery(ctx, job.Args.DeliveryID)
	if err != nil {
		return err
	}
	// 终态（含 failed/dead）不再自动推进；replay 需先把状态重置为 queued。
	if outbox.IsTerminal(delivery.Status) {
		return nil
	}
	selection, state, err := w.Providers.Resolve(ctx)
	if err != nil {
		return err
	}
	attempt := delivery.AttemptCount + 1
	switch state {
	case extensionsruntime.MailProviderUnconfigured:
		return w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliverySkipped, Reason: "provider_unconfigured", AttemptCount: attempt})
	case extensionsruntime.MailProviderUnavailable:
		maxAttempts := 5
		if job.JobRow != nil && job.MaxAttempts > 0 {
			maxAttempts = job.MaxAttempts
		}
		if outbox.ExhaustedAttempts(attempt, maxAttempts) {
			return w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliveryFailed, Reason: "provider_unavailable", ErrorSummary: "mail provider is unavailable", AttemptCount: attempt})
		}
		_ = w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliverySending, Reason: "provider_unavailable", ErrorSummary: "mail provider is unavailable", AttemptCount: attempt})
		return fmt.Errorf("mail provider %s is temporarily unavailable", selection.ExtensionID)
	}
	if w.Sender == nil {
		return fmt.Errorf("mail provider sender is not configured")
	}
	if err := w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliverySending, ExtensionID: selection.ExtensionID, AttemptCount: attempt}); err != nil {
		return err
	}
	if refresher, ok := w.Sender.(interface {
		RefreshMailProvider(context.Context, string) error
	}); ok {
		if err := refresher.RefreshMailProvider(ctx, selection.ExtensionID); err != nil {
			return err
		}
	}
	response, err := w.Sender.SendMail(ctx, selection.ExtensionID, renderDelivery(delivery))
	if err != nil {
		return err
	}
	update := notifications.DeliveryUpdate{ID: delivery.ID, ExtensionID: selection.ExtensionID, AttemptCount: attempt, Reason: response.Reason, ErrorSummary: response.Message}
	if response.OK {
		update.Status = notifications.DeliverySent
		return w.Store.UpdateDelivery(ctx, update)
	}
	if response.Classification == "temporary" {
		_ = w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliverySending, ExtensionID: selection.ExtensionID, AttemptCount: attempt, Reason: response.Reason, ErrorSummary: response.Message})
		return fmt.Errorf("temporary mail provider failure: %s", response.Reason)
	}
	update.Status = notifications.DeliveryFailed
	return w.Store.UpdateDelivery(ctx, update)
}

func Register(registry *supportjobs.Registry, worker *DeliverMailWorker) {
	registry.Add(func(workers *river.Workers) error { return river.AddWorkerSafely[DeliverMailArgs](workers, worker) })
}
