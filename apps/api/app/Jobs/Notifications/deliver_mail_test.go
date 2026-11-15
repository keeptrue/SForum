package notificationjobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	"github.com/keeptrue/sforum/apps/api/app/Support/Outbox"
)

func TestDeliverMailSkipsWithoutProvider(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued}}
	worker := DeliverMailWorker{Store: store, Providers: fakeProviderResolver{state: extensionsruntime.MailProviderUnconfigured}}
	if err := worker.Work(context.Background(), mailJob(0)); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != notifications.DeliverySkipped || store.update.Reason != "provider_unconfigured" {
		t.Fatalf("unexpected update: %#v", store.update)
	}
}

func TestDeliverMailRetriesTemporaryProviderFailure(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued, Recipient: "u@example.com"}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderReady},
		Sender:    fakeSender{response: extensionsruntime.MailProviderResponse{Classification: "temporary", Reason: "smtp.transport_failed"}},
	}
	if err := worker.Work(context.Background(), mailJob(0)); err == nil {
		t.Fatal("expected retryable error")
	}
}

func TestDeliverMailRetriesUnavailableProvider(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued, Recipient: "u@example.com"}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderUnavailable},
	}
	if err := worker.Work(context.Background(), mailJob(0)); err == nil {
		t.Fatal("expected retryable error while the provider is unavailable")
	}
	if store.update.Status != notifications.DeliverySending || store.update.Reason != "provider_unavailable" {
		t.Fatalf("unexpected transient update: %#v", store.update)
	}
}

func TestDeliverMailFailsAfterProviderUnavailableBudget(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliverySending, Recipient: "u@example.com", AttemptCount: 4}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderUnavailable},
	}
	if err := worker.Work(context.Background(), mailJob(5)); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != notifications.DeliveryFailed || store.update.Reason != "provider_unavailable" {
		t.Fatalf("unexpected terminal update: %#v", store.update)
	}
}

// 第 5 次（即最后一次）调度仍收到 temporary 时，River 不会再重试。
// 记录必须落终态 failed，否则邮件与投递记录会永久停在 sending（RV-50）。
func TestDeliverMailFailsTemporaryDeliveryAfterAttemptBudget(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliverySending, Recipient: "u@example.com", AttemptCount: 4}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderReady},
		Sender:    fakeSender{response: extensionsruntime.MailProviderResponse{Classification: "temporary", Reason: "smtp.transport_failed"}},
	}
	if err := worker.Work(context.Background(), mailJob(5)); err != nil {
		t.Fatalf("final temporary attempt must not stay retryable: %v", err)
	}
	if store.update.Status != notifications.DeliveryFailed || store.update.Reason != "provider_temporary" {
		t.Fatalf("unexpected terminal update after exhausted temporary budget: %#v", store.update)
	}
	if outbox.IsTerminal(store.update.Status) != true {
		t.Fatalf("delivery status %q is not terminal", store.update.Status)
	}
}

func TestDeliverMailSendsWhenProviderReady(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued, Recipient: "u@example.com"}}
	worker := DeliverMailWorker{
		Store:     store,
		Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, state: extensionsruntime.MailProviderReady},
		Sender:    fakeSender{response: extensionsruntime.MailProviderResponse{OK: true}},
	}
	if err := worker.Work(context.Background(), mailJob(0)); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != notifications.DeliverySent || store.update.ExtensionID != "smtp" {
		t.Fatalf("unexpected sent update: %#v", store.update)
	}
}

func mailJob(attempt int) *river.Job[DeliverMailArgs] {
	return &river.Job[DeliverMailArgs]{
		JobRow: &rivertype.JobRow{Attempt: attempt, MaxAttempts: 5},
		Args:   DeliverMailArgs{DeliveryID: 41},
	}
}

type fakeDeliveryStore struct {
	delivery notifications.MailDelivery
	update   notifications.DeliveryUpdate
}

func (s *fakeDeliveryStore) GetDelivery(context.Context, int64) (notifications.MailDelivery, error) {
	return s.delivery, nil
}
func (s *fakeDeliveryStore) UpdateDelivery(_ context.Context, update notifications.DeliveryUpdate) error {
	s.update = update
	return nil
}

type fakeProviderResolver struct {
	selection extensionsruntime.MailProviderSelection
	state     extensionsruntime.MailProviderState
}

func (r fakeProviderResolver) Resolve(context.Context) (extensionsruntime.MailProviderSelection, extensionsruntime.MailProviderState, error) {
	return r.selection, r.state, nil
}

type fakeSender struct {
	response extensionsruntime.MailProviderResponse
}

func (s fakeSender) SendMail(context.Context, string, extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error) {
	return s.response, nil
}
