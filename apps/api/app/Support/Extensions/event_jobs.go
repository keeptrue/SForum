package extensionsruntime

import (
	"context"
	"fmt"

	"github.com/riverqueue/river"

	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

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
