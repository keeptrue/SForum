package extensionsruntime

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

// observe 投递的三种装配组合，以及各自的原子性口径（P3-9）：
//
//	1. dispatcher == nil                → 进程内 `host.Deliver`（生产默认）。建行与投递
//	   仍非原子：崩溃窗口留下的 queued/running 行由周期 sweeper
//	   （`Models/Extensions/deliverysweep`，schedule `extensions.sweep_stale_deliveries`）
//	   收成 failed，不会永久停在半路。
//	2. dispatcher != nil && store == nil → 只有入队，没有需要原子的行。
//	3. dispatcher != nil && store != nil → 建行与入队必须同事务。
//	   **有 store 有 dispatcher 但没配 Pool（txBeginner==nil）时显式失败**
//	   （reason `extension.delivery_atomic_unavailable`），绝不退回两次独立往返：
//	   静默退回等于把崩溃窗口重新打开，而配置错误应当立刻可见。
//
// 这些 reason 键（`extension.delivery_record_failed` / `delivery_enqueue_failed` /
// `delivery_atomic_unavailable`）只出现在事件结果与投递账本里，不进 HTTP 响应或对外
// 错误契约，因此不配 Localization 翻译键。
type EventDeliveryArgs struct {
	DeliveryID        int64          `json:"delivery_id" river:"unique"`
	ExtensionID       string         `json:"extension_id"`
	EventName         string         `json:"event_name"`
	EventKind         string         `json:"event_kind"`
	CorrelationID     string         `json:"correlation_id"`
	Payload           map[string]any `json:"payload,omitempty"`
	PatchFields       []string       `json:"patch_fields,omitempty"`
	DeclarationID     string         `json:"declaration_id,omitempty"`
	HookID            string         `json:"hook_id,omitempty"`
	ContractVersion   string         `json:"contract_version,omitempty"`
	RuntimeInstanceID string         `json:"runtime_instance_id,omitempty"`
	PackageDigest     string         `json:"package_digest,omitempty"`
}

func (EventDeliveryArgs) Kind() string {
	return "extension.event_delivery"
}

func (EventDeliveryArgs) EnqueueOptions() supportjobs.EnqueueOptions {
	return supportjobs.EnqueueOptions{
		Queue:       supportjobs.QueueDefault,
		MaxAttempts: 3,
		Unique:      river.UniqueOpts{ByArgs: true},
	}
}

type EventDeliveryWorker struct {
	river.WorkerDefaults[EventDeliveryArgs]
	Manager *Manager
}

func (w *EventDeliveryWorker) Work(ctx context.Context, job *river.Job[EventDeliveryArgs]) error {
	if w.Manager == nil {
		return fmt.Errorf("extension event delivery worker requires manager")
	}
	if job.Args.HookID != "" {
		result := w.Manager.deliverVersionedHook(ctx, job.Args)
		if !result.OK {
			return fmt.Errorf("extension hook delivery failed: %s", result.Reason)
		}
		return nil
	}
	envelope := appevents.Envelope{
		Name:          job.Args.EventName,
		Kind:          job.Args.EventKind,
		CorrelationID: job.Args.CorrelationID,
		Payload:       job.Args.Payload,
		PatchFields:   job.Args.PatchFields,
	}
	result := w.Manager.Deliver(ctx, job.Args.ExtensionID, job.Args.DeliveryID, envelope)
	if !result.OK {
		return fmt.Errorf("extension event delivery failed: %s", result.Reason)
	}
	return nil
}

func RegisterEventDeliveryWorker(registry *supportjobs.Registry, manager *Manager) {
	registry.Add(func(workers *river.Workers) error {
		return river.AddWorkerSafely[EventDeliveryArgs](workers, &EventDeliveryWorker{Manager: manager})
	})
}

type JobDispatcherAdapter struct {
	Dispatcher *supportjobs.Dispatcher
}

func (a JobDispatcherAdapter) Enqueue(ctx context.Context, args EventDeliveryArgs, opts supportjobs.EnqueueOptions) error {
	if a.Dispatcher == nil {
		return nil
	}
	_, err := a.Dispatcher.Enqueue(ctx, args, opts)
	return err
}

// EnqueueTx 复用 Support/Jobs 既有的 River InsertTx 能力（与 Notifications outbox 同一条路径），
// 让 delivery 行与 job 在同一事务里提交。
func (a JobDispatcherAdapter) EnqueueTx(ctx context.Context, tx pgx.Tx, args EventDeliveryArgs, opts supportjobs.EnqueueOptions) error {
	if a.Dispatcher == nil {
		return nil
	}
	_, err := a.Dispatcher.EnqueueTx(ctx, tx, args, opts)
	return err
}

// emitObserve 投递 observe 事件：每个 listener 一条 delivery 行 + 一个 River job。
//
// 建行与入队必须原子（P3-9）。两次独立往返的崩溃窗口（建行成功、入队前进程死）
// 会留下永久 queued 且没有 sweeper 能回收的孤儿行，所以有队列时走
// pool.Begin → CreateEventDeliveryTx → EnqueueTx → Commit，任一步失败都回滚，
// 结果只有「行与 job 都在」或「都不在」两种。
//
// Dispatcher 与 DeliveryStore 同时存在但没配事务池时直接失败
// （extension.delivery_atomic_unavailable），不退回非原子路径：把配置错误暴露出来，
// 而不是悄悄留回崩溃窗口。
func (m *managerCore) emitObserve(ctx context.Context, envelope appevents.Envelope) appevents.Result {
	for _, listener := range m.hooks.Listeners(envelope.Name) {
		args := EventDeliveryArgs{
			ExtensionID:   listener.ID,
			EventName:     envelope.Name,
			EventKind:     envelope.Kind,
			CorrelationID: envelope.CorrelationID,
			Payload:       envelope.Payload,
			PatchFields:   envelope.PatchFields,
		}
		if m.dispatcher == nil {
			// 无队列：保持既有 in-process 投递路径。
			deliveryID := int64(0)
			if m.deliveryStore != nil {
				delivery, err := m.deliveryStore.CreateEventDelivery(ctx, extensions.EventDeliveryInput{
					ExtensionID:   listener.ID,
					EventName:     envelope.Name,
					EventKind:     envelope.Kind,
					Status:        extensions.DeliveryQueued,
					CorrelationID: envelope.CorrelationID,
				})
				if err != nil {
					return appevents.Result{OK: false, Reason: "extension.delivery_record_failed", Message: err.Error()}
				}
				deliveryID = delivery.ID
			}
			m.host.Deliver(ctx, listener.ID, deliveryID, envelope)
			continue
		}
		if m.deliveryStore == nil {
			// 无投递账本：没有需要原子的行，单次入队即可。
			if err := m.dispatcher.Enqueue(ctx, args, args.EnqueueOptions()); err != nil {
				return appevents.Result{OK: false, Reason: "extension.delivery_enqueue_failed", Message: err.Error()}
			}
			continue
		}
		if m.txBeginner == nil {
			return appevents.Result{
				OK:      false,
				Reason:  "extension.delivery_atomic_unavailable",
				Message: "Extension event delivery requires a transactional pool: delivery row and job enqueue must commit together.",
			}
		}
		tx, err := m.txBeginner.Begin(ctx)
		if err != nil {
			return appevents.Result{OK: false, Reason: "extension.delivery_record_failed", Message: err.Error()}
		}
		delivery, err := m.deliveryStore.CreateEventDeliveryTx(ctx, tx, extensions.EventDeliveryInput{
			ExtensionID:   listener.ID,
			EventName:     envelope.Name,
			EventKind:     envelope.Kind,
			Status:        extensions.DeliveryQueued,
			CorrelationID: envelope.CorrelationID,
		})
		if err != nil {
			_ = tx.Rollback(ctx)
			return appevents.Result{OK: false, Reason: "extension.delivery_record_failed", Message: err.Error()}
		}
		args.DeliveryID = delivery.ID
		// EnqueueOptions 与 Enqueue 路径完全相同，River 的 Unique.ByArgs 去重口径不变。
		if err := m.dispatcher.EnqueueTx(ctx, tx, args, args.EnqueueOptions()); err != nil {
			_ = tx.Rollback(ctx)
			return appevents.Result{OK: false, Reason: "extension.delivery_enqueue_failed", Message: err.Error()}
		}
		if err := tx.Commit(ctx); err != nil {
			_ = tx.Rollback(ctx)
			return appevents.Result{OK: false, Reason: "extension.delivery_record_failed", Message: err.Error()}
		}
	}
	return appevents.Result{OK: true}
}

func (m *managerCore) beginSyncDelivery(ctx context.Context, extensionID string, envelope appevents.Envelope) int64 {
	if m.deliveryStore == nil {
		return 0
	}
	delivery, err := m.deliveryStore.CreateEventDelivery(ctx, extensions.EventDeliveryInput{
		ExtensionID:   extensionID,
		EventName:     envelope.Name,
		EventKind:     envelope.Kind,
		Status:        extensions.DeliveryRunning,
		CorrelationID: envelope.CorrelationID,
	})
	if err != nil {
		return 0
	}
	return delivery.ID
}
