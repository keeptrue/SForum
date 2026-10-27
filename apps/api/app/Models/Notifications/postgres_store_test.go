package notifications

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresStoreCreateBundleUsesDeterministicKeys(t *testing.T) {
	runner := newMemoryRunner()
	store := newPostgresStore(runner)
	input := CreateBundleInput{
		Notification: CreateInput{RecipientUserID: 7, Type: TypeMention, TargetType: "comment", TargetID: 42, DedupeKey: "comment:42:mention:7"},
		Delivery:     CreateDeliveryInput{Recipient: "member@example.com", TemplateKey: "forum.mention", IdempotencyKey: "comment:42:mention:7"},
	}

	first, err := store.CreateBundleTx(context.Background(), runner, input)
	if err != nil {
		t.Fatalf("create first bundle: %v", err)
	}
	second, err := store.CreateBundleTx(context.Background(), runner, input)
	if err != nil {
		t.Fatalf("create duplicate bundle: %v", err)
	}
	if first.Notification.ID != second.Notification.ID || first.Delivery.ID != second.Delivery.ID {
		t.Fatalf("duplicate keys created new records: first=%#v second=%#v", first, second)
	}
	if first.Delivery.Status != DeliveryQueued {
		t.Fatalf("unexpected delivery status: %s", first.Delivery.Status)
	}
}

type memoryRunner struct {
	nextID        int64
	notifications map[string]Notification
	deliveries    map[string]MailDelivery
}

func newMemoryRunner() *memoryRunner {
	return &memoryRunner{nextID: 1, notifications: map[string]Notification{}, deliveries: map[string]MailDelivery{}}
}

func (m *memoryRunner) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "INSERT INTO notifications") {
		key := args[9].(string)
		item, ok := m.notifications[key]
		created := !ok
		if !ok {
			item = Notification{ID: m.nextID, RecipientUserID: args[0].(int64), Type: args[1].(string), Category: args[2].(string), TypeVersion: args[3].(int), PayloadVersion: args[4].(int), ActorUserID: args[5].(*int64), TargetType: args[6].(string), TargetID: args[7].(int64), Payload: args[8].(json.RawMessage), DedupeKey: key, CreatedAt: time.Now()}
			m.nextID++
			m.notifications[key] = item
		}
		return memoryRow{values: []any{item.ID, item.RecipientUserID, item.Type, item.Category, item.TypeVersion, item.PayloadVersion, item.ActorUserID, item.TargetType, item.TargetID, item.Payload, item.DedupeKey, item.ReadAt, item.CreatedAt, created}}
	}
	if strings.Contains(query, "INSERT INTO mail_deliveries") {
		key := args[3].(string)
		item, ok := m.deliveries[key]
		if !ok {
			now := time.Now()
			item = MailDelivery{ID: m.nextID, Recipient: args[0].(string), TemplateKey: args[1].(string), TemplateData: args[2].(json.RawMessage), IdempotencyKey: key, CorrelationID: args[4].(string), Status: DeliveryQueued, CreatedAt: now, UpdatedAt: now}
			m.nextID++
			m.deliveries[key] = item
		}
		return memoryRow{values: []any{item.ID, item.Recipient, item.TemplateKey, item.TemplateData, item.IdempotencyKey, item.CorrelationID, item.Status, item.ExtensionID, item.AttemptCount, item.Reason, item.ErrorSummary, item.CreatedAt, item.UpdatedAt, item.CompletedAt}}
	}
	if strings.Contains(query, "COUNT(*)") {
		userID := args[0].(int64)
		var count int64
		for _, item := range m.notifications {
			if item.RecipientUserID == userID && item.ReadAt == nil {
				count++
			}
		}
		return memoryRow{values: []any{count}}
	}
	if strings.Contains(query, "WHERE notifications.id=$1 AND notifications.recipient_user_id=$2") {
		id, userID := args[0].(int64), args[1].(int64)
		for _, item := range m.notifications {
			if item.ID == id && item.RecipientUserID == userID {
				return memoryRow{values: []any{
					item.ID, item.RecipientUserID, item.Type, item.Category, item.TypeVersion, item.PayloadVersion,
					item.ActorUserID, item.TargetType, item.TargetID, item.Payload, item.DedupeKey, item.ReadAt, item.CreatedAt,
					sql.NullInt64{}, sql.NullString{}, sql.NullString{}, sql.NullString{}, sql.NullInt64{},
					sql.NullInt64{}, sql.NullString{}, sql.NullInt64{}, sql.NullString{}, sql.NullString{},
				}}
			}
		}
	}
	return memoryRow{err: pgx.ErrNoRows}
}

func (m *memoryRunner) Exec(_ context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "UPDATE notifications SET read_at") {
		id, userID := args[0].(int64), args[1].(int64)
		for key, item := range m.notifications {
			if item.ID == id && item.RecipientUserID == userID {
				now := time.Now()
				item.ReadAt = &now
				m.notifications[key] = item
				return pgconn.NewCommandTag("UPDATE 1"), nil
			}
		}
	}
	return pgconn.NewCommandTag("UPDATE 0"), nil
}

func (*memoryRunner) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}

type memoryRow struct {
	values []any
	err    error
}

func (r memoryRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *int64:
			*target = r.values[i].(int64)
		case *int:
			*target = r.values[i].(int)
		case *string:
			*target = r.values[i].(string)
		case **int64:
			*target = r.values[i].(*int64)
		case **time.Time:
			*target = r.values[i].(*time.Time)
		case *time.Time:
			*target = r.values[i].(time.Time)
		case *json.RawMessage:
			*target = r.values[i].(json.RawMessage)
		case *bool:
			*target = r.values[i].(bool)
		case *sql.NullInt64:
			*target = r.values[i].(sql.NullInt64)
		case *sql.NullString:
			*target = r.values[i].(sql.NullString)
		}
	}
	return nil
}

func TestPostgresStoreMarksOnlyRecipientsNotificationRead(t *testing.T) {
	runner := newMemoryRunner()
	store := newPostgresStore(runner)
	bundle, err := store.CreateBundleTx(context.Background(), runner, CreateBundleInput{
		Notification: CreateInput{RecipientUserID: 7, Type: TypeReply, TargetType: "comment", TargetID: 9, DedupeKey: "comment:9:reply:7"},
		Delivery:     CreateDeliveryInput{Recipient: "member@example.com", TemplateKey: "forum.reply", IdempotencyKey: "comment:9:reply:7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRead(context.Background(), 8, bundle.Notification.ID); err != ErrNotificationNotFound {
		t.Fatalf("other recipient changed notification: %v", err)
	}
	if err := store.MarkRead(context.Background(), 7, bundle.Notification.ID); err != nil {
		t.Fatalf("owner mark read: %v", err)
	}
	count, err := store.UnreadCount(context.Background(), 7)
	if err != nil || count != 0 {
		t.Fatalf("unexpected unread count: count=%d err=%v", count, err)
	}
}

func TestPostgresStoreGetsOnlyRecipientsNotification(t *testing.T) {
	runner := newMemoryRunner()
	store := newPostgresStore(runner)
	created, err := store.Create(context.Background(), CreateInput{
		RecipientUserID: 7, Type: TypeReply, TargetType: "comment", TargetID: 9,
		Payload: json.RawMessage(`{"topicId":3}`), DedupeKey: "comment:9:reply:7",
	})
	if err != nil {
		t.Fatal(err)
	}
	item, err := store.GetNotification(context.Background(), 7, created.ID)
	if err != nil || item.ID != created.ID || item.RecipientUserID != 7 {
		t.Fatalf("owner detail=%#v err=%v", item, err)
	}
	if _, err := store.GetNotification(context.Background(), 8, created.ID); !errors.Is(err, ErrNotificationNotFound) {
		t.Fatalf("foreign recipient detail error=%v", err)
	}
}

func TestPostgresStoreListFiltersPreserveRecipientCursorPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	stamp := time.Now().UnixNano()
	insertUser := func(suffix string) int64 {
		username := fmt.Sprintf("notification_filter_%s_%d", suffix, stamp)
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO users (username, username_lower, email, email_lower, display_name, status)
VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`, username, username+"@example.test").Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	userID := insertUser("owner")
	otherUserID := insertUser("other")
	actorUserID := insertUser("actor")
	store := newPostgresStore(tx)
	create := func(recipient int64, typ, category, key string) Notification {
		item, err := store.Create(ctx, CreateInput{
			RecipientUserID: recipient,
			Type:            typ,
			Category:        category,
			ActorUserID:     &actorUserID,
			TargetType:      "system",
			Payload:         json.RawMessage(`{"private":"rest-only"}`),
			DedupeKey:       fmt.Sprintf("filter:%d:%s", stamp, key),
		})
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	oldReply := create(userID, TypeReply, "conversation", "old-reply")
	_ = create(userID, TypeMention, "mention", "mention")
	newReply := create(userID, TypeReply, "conversation", "new-reply")
	_ = create(otherUserID, TypeReply, "conversation", "other-user")
	if _, err := tx.Exec(ctx, `UPDATE notifications SET read_at=now() WHERE id=$1`, oldReply.ID); err != nil {
		t.Fatal(err)
	}

	first, err := store.List(ctx, ListInput{RecipientUserID: userID, Limit: 1, Type: TypeReply})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.Items[0].ID != newReply.ID || !first.HasMore {
		t.Fatalf("first filtered page=%#v", first)
	}
	if first.Items[0].Actor == nil || first.Items[0].Actor.ID != actorUserID || first.Items[0].Actor.Username == "" || first.Items[0].Actor.Avatar.Kind != "initials" {
		t.Fatalf("reply actor summary=%#v", first.Items[0].Actor)
	}
	second, err := store.List(ctx, ListInput{RecipientUserID: userID, Limit: 1, BeforeID: first.Items[0].ID, Category: "conversation", Unread: boolPointer(false)})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.Items[0].ID != oldReply.ID || second.HasMore {
		t.Fatalf("second filtered page=%#v", second)
	}
	unread, err := store.List(ctx, ListInput{RecipientUserID: userID, Limit: 10, Unread: boolPointer(true)})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range unread.Items {
		if item.RecipientUserID != userID || item.ReadAt != nil || item.ID == oldReply.ID {
			t.Fatalf("unread filter leaked row: %#v", item)
		}
	}
}

func boolPointer(value bool) *bool { return &value }
