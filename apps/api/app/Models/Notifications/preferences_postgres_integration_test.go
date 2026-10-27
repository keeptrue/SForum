package notifications

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestSiteDisabledEmailPreferenceCannotBeEnabledPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	var policyTable string
	if err := pool.QueryRow(ctx, `SELECT COALESCE(to_regclass('public.notification_type_policies')::text,'')`).Scan(&policyTable); err != nil || policyTable == "" {
		t.Skip("notification platform migrations are not applied")
	}

	stamp := time.Now().UnixNano()
	typeID := fmt.Sprintf("preference_email_gate_%d.notice", stamp)
	username := fmt.Sprintf("preference_email_gate_%d", stamp)
	var userID int64
	if err := pool.QueryRow(ctx, `INSERT INTO users (username,username_lower,email,email_lower,display_name,status)
		VALUES ($1,$1,$2,$2,$1,'active') RETURNING id`, username, username+"@example.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, userID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM notification_type_policies WHERE type=$1`, typeID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM notification_type_descriptors WHERE type=$1`, typeID)
	})

	if _, err := pool.Exec(ctx, `INSERT INTO notification_type_descriptors
		(type,contract_version,payload_version,category,active)
		VALUES ($1,1,1,'system',TRUE)`, typeID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO notification_type_policies
		(type,channel,enabled,recommended_enabled,user_configurable,required)
		VALUES ($1,'email',FALSE,FALSE,TRUE,FALSE)`, typeID); err != nil {
		t.Fatal(err)
	}

	store := NewPostgresStore(pool)
	catalog, err := store.ListPreferences(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	item, found := preferenceItem(catalog.Items, typeID, "email")
	if !found || item.Enabled || item.Effective || item.State != "inherit" {
		t.Fatalf("site-disabled email preference = %#v found=%v", item, found)
	}

	_, err = store.ReplacePreferences(ctx, userID, catalog.Revision, []PreferenceInput{{Type: typeID, Channel: "email", State: "enabled"}})
	if !errors.Is(err, ErrPreferenceInvalid) {
		t.Fatalf("site-disabled email override error=%v want=%v", err, ErrPreferenceInvalid)
	}

	if _, err := pool.Exec(ctx, `UPDATE notification_type_policies SET enabled=TRUE WHERE type=$1 AND channel='email'`, typeID); err != nil {
		t.Fatal(err)
	}
	updated, err := store.ReplacePreferences(ctx, userID, catalog.Revision, []PreferenceInput{{Type: typeID, Channel: "email", State: "enabled"}})
	if err != nil {
		t.Fatal(err)
	}
	item, found = preferenceItem(updated.Items, typeID, "email")
	if !found || !item.Enabled || !item.Effective || item.State != "enabled" {
		t.Fatalf("site-enabled email preference = %#v found=%v", item, found)
	}

	if _, err := pool.Exec(ctx, `UPDATE notification_type_policies SET enabled=FALSE WHERE type=$1 AND channel='email'`, typeID); err != nil {
		t.Fatal(err)
	}
	enabled, err := store.DeliveryEnabled(ctx, userID, typeID, "email")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("site-disabled email policy was bypassed by a saved user override")
	}
}

func preferenceItem(items []PreferenceItem, typeID, channel string) (PreferenceItem, bool) {
	for _, item := range items {
		if item.Type == typeID && item.Channel == channel {
			return item, true
		}
	}
	return PreferenceItem{}, false
}
