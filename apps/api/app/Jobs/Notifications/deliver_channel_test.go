package notificationjobs

import (
	"context"
	"testing"

	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestDeliverChannelWorkerSkipsUnavailableSubscription(t *testing.T) {
	store := &channelWorkerStore{delivery: notifications.ChannelDelivery{ID: 7, RecipientUserID: 42, Channel: "web_push", Status: notifications.DeliveryQueued}}
	worker := &DeliverChannelWorker{Store: store, Invoker: &channelWorkerInvoker{}}
	if err := worker.Work(context.Background(), &river.Job[DeliverChannelArgs]{Args: DeliverChannelArgs{DeliveryID: 7}}); err != nil {
		t.Fatal(err)
	}
	if store.update.Status != notifications.DeliverySkipped || store.update.Reason != "notification.subscription_unavailable" {
		t.Fatalf("delivery update = %#v", store.update)
	}
}

func TestDeliverChannelWorkerDoesNotReplaySentSubscription(t *testing.T) {
	store := &channelWorkerStore{
		delivery:      notifications.ChannelDelivery{ID: 7, RecipientUserID: 42, Type: "reply", Channel: "web_push", Status: notifications.DeliveryQueued},
		subscriptions: []notifications.WebPushSubscription{{ID: 1, Endpoint: "https://push.invalid/1"}, {ID: 2, Endpoint: "https://push.invalid/2"}},
		attempts:      map[int64]notifications.WebPushDeliveryAttempt{1: {DeliveryID: 7, SubscriptionID: 1, Status: notifications.DeliverySent}},
	}
	invoker := &channelWorkerInvoker{result: ChannelProviderResult{
		ExtensionID: "sforum.web-push", ArtifactDigest: "digest",
		Output: map[string]any{"ok": true, "reason": "web_push.sent"},
	}}
	worker := &DeliverChannelWorker{Store: store, Invoker: invoker}
	if err := worker.Work(context.Background(), &river.Job[DeliverChannelArgs]{Args: DeliverChannelArgs{DeliveryID: 7}}); err != nil {
		t.Fatal(err)
	}
	if invoker.calls != 1 || store.attempts[1].AttemptCount != 0 || store.attempts[2].Status != notifications.DeliverySent {
		t.Fatalf("calls=%d attempts=%#v", invoker.calls, store.attempts)
	}
	if store.update.Status != notifications.DeliverySent || store.update.ProviderArtifactDigest != "digest" {
		t.Fatalf("delivery update = %#v", store.update)
	}
}

func TestDeliverChannelWorkerRetriesTemporaryProviderFailure(t *testing.T) {
	store := &channelWorkerStore{
		delivery:      notifications.ChannelDelivery{ID: 7, RecipientUserID: 42, Type: "reply", Channel: "web_push", Status: notifications.DeliveryQueued},
		subscriptions: []notifications.WebPushSubscription{{ID: 1, Endpoint: "https://push.invalid/1"}}, attempts: map[int64]notifications.WebPushDeliveryAttempt{},
	}
	invoker := &channelWorkerInvoker{result: ChannelProviderResult{Output: map[string]any{
		"ok": false, "classification": "temporary", "reason": "web_push.service_unavailable",
	}}}
	err := (&DeliverChannelWorker{Store: store, Invoker: invoker}).Work(context.Background(), &river.Job[DeliverChannelArgs]{Args: DeliverChannelArgs{DeliveryID: 7}})
	if err == nil || store.update.Status != notifications.DeliverySending || store.attempts[1].Status != notifications.DeliveryQueued {
		t.Fatalf("err=%v update=%#v attempts=%#v", err, store.update, store.attempts)
	}
}

// Web Push 与邮件同构：最后一次调度的 temporary 必须落终态，不能停在 sending（RV-50）。
func TestDeliverChannelWorkerFailsTemporaryAfterAttemptBudget(t *testing.T) {
	store := &channelWorkerStore{
		delivery:      notifications.ChannelDelivery{ID: 7, RecipientUserID: 42, Type: "reply", Channel: "web_push", Status: notifications.DeliverySending, AttemptCount: 4},
		subscriptions: []notifications.WebPushSubscription{{ID: 1, Endpoint: "https://push.invalid/1"}}, attempts: map[int64]notifications.WebPushDeliveryAttempt{},
	}
	invoker := &channelWorkerInvoker{result: ChannelProviderResult{Output: map[string]any{
		"ok": false, "classification": "temporary", "reason": "web_push.service_unavailable",
	}}}
	err := (&DeliverChannelWorker{Store: store, Invoker: invoker}).Work(context.Background(), &river.Job[DeliverChannelArgs]{
		JobRow: &rivertype.JobRow{Attempt: 5, MaxAttempts: 5},
		Args:   DeliverChannelArgs{DeliveryID: 7},
	})
	if err != nil {
		t.Fatalf("final temporary attempt must not stay retryable: %v", err)
	}
	if store.update.Status != notifications.DeliveryFailed || store.update.Reason != "notification.provider_temporary" {
		t.Fatalf("unexpected terminal update after exhausted temporary budget: %#v", store.update)
	}
}

type channelWorkerStore struct {
	delivery      notifications.ChannelDelivery
	subscriptions []notifications.WebPushSubscription
	attempts      map[int64]notifications.WebPushDeliveryAttempt
	update        notifications.ChannelDeliveryUpdate
}

func (s *channelWorkerStore) GetChannelDelivery(context.Context, int64) (notifications.ChannelDelivery, error) {
	return s.delivery, nil
}
func (s *channelWorkerStore) UpdateChannelDelivery(_ context.Context, update notifications.ChannelDeliveryUpdate) error {
	s.update = update
	return nil
}
func (s *channelWorkerStore) ListWebPushSubscriptions(context.Context, int64, bool) ([]notifications.WebPushSubscription, error) {
	return s.subscriptions, nil
}
func (s *channelWorkerStore) GetWebPushDeliveryAttempt(_ context.Context, deliveryID, subscriptionID int64) (notifications.WebPushDeliveryAttempt, error) {
	if s.attempts == nil {
		s.attempts = map[int64]notifications.WebPushDeliveryAttempt{}
	}
	if item, ok := s.attempts[subscriptionID]; ok {
		return item, nil
	}
	item := notifications.WebPushDeliveryAttempt{DeliveryID: deliveryID, SubscriptionID: subscriptionID, Status: notifications.DeliveryQueued}
	s.attempts[subscriptionID] = item
	return item, nil
}
func (s *channelWorkerStore) UpdateWebPushDeliveryAttempt(_ context.Context, item notifications.WebPushDeliveryAttempt) error {
	s.attempts[item.SubscriptionID] = item
	return nil
}
func (*channelWorkerStore) MarkWebPushSubscriptionFailed(context.Context, int64, string) error {
	return nil
}

type channelWorkerInvoker struct {
	result ChannelProviderResult
	err    error
	calls  int
}

func (i *channelWorkerInvoker) InvokeNotificationChannel(context.Context, ChannelProviderRequest) (ChannelProviderResult, error) {
	i.calls++
	return i.result, i.err
}
