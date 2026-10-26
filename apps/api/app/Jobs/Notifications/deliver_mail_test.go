package notificationjobs

import (
	"context"
	"testing"

	"github.com/riverqueue/river"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
)

func TestDeliverMailSkipsWithoutProvider(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued}}
	worker := DeliverMailWorker{Store: store, Providers: fakeProviderResolver{}}
	if err := worker.Work(context.Background(), &river.Job[DeliverMailArgs]{Args: DeliverMailArgs{DeliveryID: 41}}); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != notifications.DeliverySkipped || store.update.Reason != "provider_unavailable" {
		t.Fatalf("unexpected update: %#v", store.update)
	}
}

func TestDeliverMailRetriesTemporaryProviderFailure(t *testing.T) {
	store := &fakeDeliveryStore{delivery: notifications.MailDelivery{ID: 41, Status: notifications.DeliveryQueued, Recipient: "u@example.com"}}
	worker := DeliverMailWorker{Store: store, Providers: fakeProviderResolver{selection: extensionsruntime.MailProviderSelection{ExtensionID: "smtp"}, ok: true}, Sender: fakeSender{response: extensionsruntime.MailProviderResponse{Classification: "temporary", Reason: "smtp.transport_failed"}}}
	if err := worker.Work(context.Background(), &river.Job[DeliverMailArgs]{Args: DeliverMailArgs{DeliveryID: 41}}); err == nil {
		t.Fatal("expected retryable error")
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
	ok        bool
}

func (r fakeProviderResolver) Selected(context.Context) (extensionsruntime.MailProviderSelection, bool, error) {
	return r.selection, r.ok, nil
}

type fakeSender struct {
	response extensionsruntime.MailProviderResponse
}

func (s fakeSender) SendMail(context.Context, string, extensionsruntime.MailProviderRequest) (extensionsruntime.MailProviderResponse, error) {
	return s.response, nil
}
