package extensionsruntime

import (
	"context"
	"testing"

	"github.com/riverqueue/river"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func TestEventDeliveryArgsKindAndOptions(t *testing.T) {
	args := EventDeliveryArgs{DeliveryID: 9}
	if args.Kind() != "extension.event_delivery" {
		t.Fatalf("unexpected kind: %q", args.Kind())
	}
	opts := args.EnqueueOptions()
	if opts.MaxAttempts != 3 || !opts.Unique.ByArgs {
		t.Fatalf("unexpected options: %#v", opts)
	}
}

func TestEventDeliveryWorkerCallsManager(t *testing.T) {
	store := &fakeDeliveryStore{}
	bus := NewHookBus(HookBusConfig{Invoker: HookInvokerFunc(func(context.Context, extensions.Extension, HookInput) HookResult {
		return HookResult{OK: true}
	})})
	manager := NewManager(ManagerConfig{HookBus: bus, DeliveryStore: store})
	extension := runtimeExtension("worker.plugin")
	extension.Manifest.Events = []extensions.ManifestEvent{{Name: appevents.TopicCreated, Kind: appevents.KindObserve}}
	if err := manager.Start(context.Background(), extension); err != nil {
		t.Fatalf("start: %v", err)
	}
	delivery, err := store.CreateEventDelivery(context.Background(), extensions.EventDeliveryInput{
		ExtensionID:   extension.ID,
		EventName:     appevents.TopicCreated,
		EventKind:     appevents.KindObserve,
		Status:        extensions.DeliveryQueued,
		CorrelationID: "corr-worker",
	})
	if err != nil {
		t.Fatalf("create delivery: %v", err)
	}

	worker := EventDeliveryWorker{Manager: manager}
	err = worker.Work(context.Background(), &river.Job[EventDeliveryArgs]{
		Args: EventDeliveryArgs{
			DeliveryID:    delivery.ID,
			ExtensionID:   extension.ID,
			EventName:     appevents.TopicCreated,
			EventKind:     appevents.KindObserve,
			CorrelationID: "corr-worker",
		},
	})
	if err != nil {
		t.Fatalf("work: %v", err)
	}
	if store.deliveries[0].Status != extensions.DeliverySucceeded {
		t.Fatalf("expected succeeded delivery, got %#v", store.deliveries[0])
	}
}
