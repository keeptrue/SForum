package migrations

import (
	"context"
	"fmt"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const notificationWebPushDefaultsMigration = "202608020001_notification_web_push_recommended_defaults.sql"

func TestNotificationWebPushDefaultsMigrationPreservesOperatorChoices(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("notification_web_push_defaults_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	if _, err := pool.Exec(ctx, `
		CREATE TABLE notification_type_policies (
			type TEXT NOT NULL,
			channel TEXT NOT NULL,
			enabled BOOLEAN NOT NULL,
			recommended_enabled BOOLEAN NOT NULL,
			revision BIGINT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
			PRIMARY KEY (type, channel)
		);
		CREATE TABLE notification_policy_revisions (
			singleton BOOLEAN PRIMARY KEY,
			revision BIGINT NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
		);
		INSERT INTO notification_policy_revisions VALUES (TRUE, 7, now());
		INSERT INTO notification_type_policies (type, channel, enabled, recommended_enabled, revision) VALUES
			('reply', 'web_push', FALSE, FALSE, 1),
			('mention', 'web_push', FALSE, FALSE, 2),
			('moderation_approved', 'web_push', TRUE, FALSE, 2),
			('moderation_rejected', 'web_push', FALSE, TRUE, 2),
			('admin_test', 'web_push', FALSE, FALSE, 1),
			('reply', 'email', FALSE, FALSE, 1);
	`); err != nil {
		t.Fatal(err)
	}

	body, err := fs.ReadFile(Files(), notificationWebPushDefaultsMigration)
	if err != nil {
		t.Fatal(err)
	}
	up, _, found := strings.Cut(string(body), "-- +goose Down")
	if !found {
		t.Fatal("Web Push defaults migration has no Down section")
	}
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("apply Web Push defaults migration: %v", err)
	}

	rows, err := pool.Query(ctx, `SELECT type, channel, enabled, recommended_enabled, revision FROM notification_type_policies ORDER BY type, channel`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var typ, channel string
		var enabled, recommended bool
		var revision int64
		if err := rows.Scan(&typ, &channel, &enabled, &recommended, &revision); err != nil {
			t.Fatal(err)
		}
		got[typ+":"+channel] = fmt.Sprintf("%t/%t/%d", enabled, recommended, revision)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"admin_test:web_push":          "false/false/1",
		"mention:web_push":             "false/false/2",
		"moderation_approved:web_push": "true/false/2",
		"moderation_rejected:web_push": "false/true/2",
		"reply:email":                  "false/false/1",
		"reply:web_push":               "true/true/2",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("policies=%v want=%v", got, want)
	}
	var policyRevision int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM notification_policy_revisions WHERE singleton=TRUE`).Scan(&policyRevision); err != nil {
		t.Fatal(err)
	}
	if policyRevision != 8 {
		t.Fatalf("policy revision=%d want=8", policyRevision)
	}
	if _, err := pool.Exec(ctx, stripSQLComments(up)); err != nil {
		t.Fatalf("rerun Web Push defaults migration: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT revision FROM notification_policy_revisions WHERE singleton=TRUE`).Scan(&policyRevision); err != nil {
		t.Fatal(err)
	}
	if policyRevision != 8 {
		t.Fatalf("idempotent policy revision=%d want=8", policyRevision)
	}
}
