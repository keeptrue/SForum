package notificationjobs

import (
	"context"
	"fmt"
	"log/slog"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
	"github.com/riverqueue/river"
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
	// Logger 为零值时退回 slog.Default()；落库失败必须有日志，不能静默。
	Logger *slog.Logger
}

func (w *DeliverMailWorker) logger() *slog.Logger {
	if w != nil && w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
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
	attempt := mailAttempt(job, delivery.AttemptCount)
	budget := mailAttemptBudget(job)
	switch state {
	case extensionsruntime.MailProviderUnconfigured:
		return w.Store.UpdateDelivery(ctx, notifications.DeliveryUpdate{ID: delivery.ID, Status: notifications.DeliverySkipped, Reason: "provider_unconfigured", AttemptCount: attempt})
	case extensionsruntime.MailProviderUnavailable:
		return w.settleOrRetry(ctx, notifications.DeliveryUpdate{
			ID: delivery.ID, Reason: "provider_unavailable", ErrorSummary: "mail provider is unavailable", AttemptCount: attempt,
		}, fmt.Errorf("mail provider %s is temporarily unavailable", selection.ExtensionID), budget)
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
			return w.settleOrRetry(ctx, notifications.DeliveryUpdate{
				ID: delivery.ID, ExtensionID: selection.ExtensionID, AttemptCount: attempt,
				Reason: "provider_refresh_failed", ErrorSummary: truncateDeliverySummary(err.Error()),
			}, err, budget)
		}
	}
	response, err := w.Sender.SendMail(ctx, selection.ExtensionID, renderDelivery(delivery))
	if err != nil {
		// 发送通道本身的错误（SMTP 断连、provider 崩溃）同样是可重试失败：
		// 最后一次调度必须落终态，否则记录永久停在 sending（RV-50）。
		return w.settleOrRetry(ctx, notifications.DeliveryUpdate{
			ID: delivery.ID, ExtensionID: selection.ExtensionID, AttemptCount: attempt,
			Reason: "provider_send_error", ErrorSummary: truncateDeliverySummary(err.Error()),
		}, err, budget)
	}
	update := notifications.DeliveryUpdate{ID: delivery.ID, ExtensionID: selection.ExtensionID, AttemptCount: attempt, Reason: response.Reason, ErrorSummary: response.Message}
	if response.OK {
		update.Status = notifications.DeliverySent
		return w.Store.UpdateDelivery(ctx, update)
	}
	if response.Classification == "temporary" {
		providerReason := response.Reason
		update.Reason = "provider_temporary"
		return w.settleOrRetry(ctx, update, fmt.Errorf("temporary mail provider failure: %s", providerReason), budget)
	}
	update.Status = notifications.DeliveryFailed
	return w.Store.UpdateDelivery(ctx, update)
}

// mailAttempt 取本次调度在 River 里的真实次数。
//
// 不能只用 delivery.AttemptCount+1：那是上一次「写 sending」成功后的持久值，
// 写库失败时它会落后于 River 的真实次数，预算就永远用不完（RV-50 的连带项）。
func mailAttempt(job *river.Job[DeliverMailArgs], persisted int) int {
	if job != nil && job.JobRow != nil && job.Attempt > 0 {
		return job.Attempt
	}
	return persisted + 1
}

// mailAttemptBudget 取自 River job 的 MaxAttempts；EnqueueOptions 默认也是 5。
// job row 缺失（直接构造的测试 job）时回落到默认值。
func mailAttemptBudget(job *river.Job[DeliverMailArgs]) int {
	if job != nil && job.JobRow != nil && job.MaxAttempts > 0 {
		return job.MaxAttempts
	}
	return 5
}

// settleOrRetry 统一处理可重试失败的落库：
// 还有预算就写 sending 并返回错误交给 River；预算用尽则写 failed 终态并吞掉错误。
// 终态是这里的硬要求：没有 reaper，停在 sending 的记录只能手工改库。
func (w *DeliverMailWorker) settleOrRetry(ctx context.Context, update notifications.DeliveryUpdate, retryErr error, maxAttempts int) error {
	if outbox.ExhaustedAttempts(update.AttemptCount, maxAttempts) {
		update.Status = notifications.DeliveryFailed
		if err := w.Store.UpdateDelivery(ctx, update); err != nil {
			// 终态落库失败：**不**把错误抛回 River。抛回去会让 job 再跑一次，
			// 而那意味着可能重复发送；留成 queued/sending 由 mail.sweep_stale 兜底
			// 收成 failed（P2-6），这里只保证信号不丢。
			w.logger().Error("mail delivery terminal write failed",
				"deliveryId", update.ID, "attempt", update.AttemptCount, "status", update.Status, "error", err)
			return nil
		}
		return nil
	}
	update.Status = notifications.DeliverySending
	if err := w.Store.UpdateDelivery(ctx, update); err != nil {
		// 进度落库失败：attempt 预算会落后于 River 的真实次数（RV-50 的连带项），
		// 必须留痕，否则「为什么这条一直重试」无从查起。
		w.logger().Error("mail delivery progress write failed",
			"deliveryId", update.ID, "attempt", update.AttemptCount, "status", update.Status, "error", err)
	}
	return retryErr
}

// truncateDeliverySummary 把 provider 的错误文本裁到 error_summary 的列宽语义内。
func truncateDeliverySummary(value string) string {
	return truncateUTF8(value, errorSummaryLimit)
}

func Register(registry *supportjobs.Registry, worker *DeliverMailWorker) {
	registry.Add(func(workers *river.Workers) error { return river.AddWorkerSafely[DeliverMailArgs](workers, worker) })
}
