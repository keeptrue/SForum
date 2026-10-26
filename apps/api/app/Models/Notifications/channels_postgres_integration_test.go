package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
)

type channelProjectionJobs struct{ calls int }

func (j *channelProjectionJobs) EnqueueTx(context.Context, pgx.Tx, river.JobArgs, supportjobs.EnqueueOptions) (*rivertype.JobInsertResult, error) {
	j.calls++
	return nil, nil
}

func TestExternalOnlyProjectionCreatesNoInboxRowPostgres(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	userID := insertNotificationChannelUser(t, ctx, tx, "external")
	store := newPostgresStore(tx)
	jobs := &channelProjectionJobs{}
	outbox := NewOutbox(nil, store, jobs)
	dedupe := fmt.Sprintf("external-only:%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `UPDATE notification_type_policies
SET enabled=CASE WHEN channel='web_push' THEN TRUE ELSE FALSE END,
    recommended_enabled=CASE WHEN channel='web_push' THEN TRUE ELSE FALSE END
WHERE type=$1`, TypeReply); err != nil {
		t.Fatal(err)
	}

	err := outbox.CreateProjectionsTx(ctx, tx, CreateBundleInput{
		Notification: CreateInput{
			RecipientUserID: userID,
			Type:            TypeReply,
			Category:        "conversation",
			PayloadVersion:  1,
			TargetType:      "comment",
			TargetID:        91,
			Payload:         json.RawMessage(`{"title":"Reply","body":"A reply arrived"}`),
			DedupeKey:       dedupe,
		},
		Delivery: CreateDeliveryInput{
			Recipient:      "external-only@example.test",
			TemplateKey:    "forum.reply",
			IdempotencyKey: dedupe,
		},
		Channels: []string{"in_app", "email", "web_push"},
	})
	if err != nil {
		t.Fatal(err)
	}

	var inboxCount, mailCount int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notifications WHERE dedupe_key=$1`, dedupe).Scan(&inboxCount); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM mail_deliveries WHERE idempotency_key=$1`, dedupe).Scan(&mailCount); err != nil {
		t.Fatal(err)
	}
	var notificationID *int64
	var payload, target json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT notification_id,payload,target_meta FROM notification_channel_deliveries WHERE idempotency_key=$1`, dedupe+":web_push").Scan(&notificationID, &payload, &target); err != nil {
		t.Fatal(err)
	}
	if inboxCount != 0 || mailCount != 0 || notificationID != nil || jobs.calls != 1 {
		t.Fatalf("inbox=%d mail=%d notification_id=%v jobs=%d", inboxCount, mailCount, notificationID, jobs.calls)
	}
	var targetEnvelope struct {
		TargetType string `json:"targetType"`
		TargetID   int64  `json:"targetId"`
	}
	if err := json.Unmarshal(target, &targetEnvelope); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), "A reply arrived") || targetEnvelope.TargetType != "comment" || targetEnvelope.TargetID != 91 {
		t.Fatalf("external envelope payload=%s target=%s", payload, target)
	}
}

func TestWebPushSubscriptionOwnershipReplayAndErasureCascadePostgres(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	ownerID := insertNotificationChannelUser(t, ctx, tx, "owner")
	otherID := insertNotificationChannelUser(t, ctx, tx, "other")
	store := newPostgresStore(tx)
	stamp := time.Now().UnixNano()

	subscription, err := store.CreateWebPushSubscription(ctx, CreateWebPushSubscriptionInput{
		UserID:          ownerID,
		Endpoint:        fmt.Sprintf("https://push.example.test/subscriptions/%d", stamp),
		P256DHKey:       make([]byte, 65),
		AuthKey:         make([]byte, 16),
		ContentEncoding: "aes128gcm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeWebPushSubscription(ctx, otherID, subscription.ID); err != ErrSubscriptionNotFound {
		t.Fatalf("cross-user revoke error=%v", err)
	}
	active, err := store.ListWebPushSubscriptions(ctx, ownerID, true)
	if err != nil || len(active) != 1 || active[0].ID != subscription.ID {
		t.Fatalf("owner subscription after theft attempt=%#v err=%v", active, err)
	}

	delivery, err := store.CreateChannelDeliveryTx(ctx, tx, CreateChannelDeliveryInput{
		RecipientUserID: ownerID,
		Type:            TypeReply,
		Channel:         "web_push",
		PayloadVersion:  1,
		IdempotencyKey:  fmt.Sprintf("attempt:%d", stamp),
	})
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := store.GetWebPushDeliveryAttempt(ctx, delivery.ID, subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.AttemptCount, attempt.Reason = DeliverySent, 1, "accepted"
	if err := store.UpdateWebPushDeliveryAttempt(ctx, attempt); err != nil {
		t.Fatal(err)
	}
	replay, err := store.GetWebPushDeliveryAttempt(ctx, delivery.ID, subscription.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Status != DeliverySent || replay.AttemptCount != 1 || replay.Reason != "accepted" {
		t.Fatalf("replay reset completed attempt: %#v", replay)
	}
	var attemptRows int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM web_push_delivery_attempts WHERE delivery_id=$1 AND subscription_id=$2`, delivery.ID, subscription.ID).Scan(&attemptRows); err != nil {
		t.Fatal(err)
	}
	if attemptRows != 1 {
		t.Fatalf("attempt rows=%d", attemptRows)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM users WHERE id=$1`, ownerID); err != nil {
		t.Fatal(err)
	}
	var deliveries, subscriptions, attempts int
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM notification_channel_deliveries WHERE id=$1`, delivery.ID).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM web_push_subscriptions WHERE id=$1`, subscription.ID).Scan(&subscriptions); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM web_push_delivery_attempts WHERE delivery_id=$1`, delivery.ID).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if deliveries != 0 || subscriptions != 0 || attempts != 0 {
		t.Fatalf("erasure left delivery=%d subscription=%d attempt=%d", deliveries, subscriptions, attempts)
	}
}

func TestUnreadCursorPartialIndexPostgres(t *testing.T) {
	ctx, tx := notificationChannelPostgresTx(t)
	var definition string
	if err := tx.QueryRow(ctx, `SELECT indexdef FROM pg_indexes WHERE schemaname=current_schema() AND indexname='notifications_recipient_unread_idx'`).Scan(&definition); err != nil {
		t.Fatal(err)
	}
	normalized := strings.Join(strings.Fields(strings.ToLower(definition)), " ")
	if !strings.Contains(normalized, "(recipient_user_id, id desc)") || !strings.Contains(normalized, "where (read_at is null)") {
		t.Fatalf("unexpected unread index: %s", definition)
	}
}

func notificationChannelPostgresTx(t *testing.T) (context.Context, pgx.Tx) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		databaseURL = strings.TrimSpace(os.Getenv("DATABASE_URL"))
	}
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL or DATABASE_URL is required")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return ctx, tx
}

func insertNotificationChannelUser(t *testing.T, ctx context.Context, tx pgx.Tx, suffix string) int64 {
	t.Helper()
	stamp := time.Now().UnixNano()
	username := fmt.Sprintf("notification_channel_%s_%d", suffix, stamp)
	var id int64
	if err := tx.QueryRow(ctx, `INSERT INTO users (username,username_lower,email,email_lower,display_name,status)
VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`, username, username+"@example.test").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}
