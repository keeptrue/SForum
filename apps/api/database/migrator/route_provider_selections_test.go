package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const routeProviderSelectionsVersion = int64(202607140018)

func TestRouteProviderSelectionsUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, extensionDatabaseDispositionResourcePresenceVersion); err != nil {
		t.Fatalf("migrate isolated schema to database disposition presence: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, routeProviderSelectionsVersion, true); err != nil {
		t.Fatalf("apply route provider selections: %v", err)
	}
	assertRouteProviderSelectionSchema(t, ctx, db, true)

	var actorID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES (
		  'route-selector', 'route-selector',
		  'route-selector@example.test', 'route-selector@example.test', 'Route Selector'
		) RETURNING id
	`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_route_provider_selections (
			target_route_id, target_contract_version, method, path_signature,
			provider_route_id, provider_contract_version, provider_extension_id,
			provider_extension_version_id, provider_extension_version,
			provider_package_digest, selected_by_user_id, selection_audit_event_id
		) VALUES (
			'core.route.topic.show', 'core.route.topic.show@1', 'GET', '/topics/:',
			'fixture.route.topic.show', 'fixture.route.topic.show@1', 'fixture.route',
			42, '1.0.0', repeat('a', 64), $1, 77
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_route_provider_selection_events (
			target_route_id, target_contract_version, method, path_signature,
			action, selected_provider, actor_user_id, audit_event_id, selection_revision
		) VALUES (
			'core.route.topic.show', 'core.route.topic.show@1', 'GET', '/topics/:',
			'select', '{"extensionId":"fixture.route","packageDigest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}'::jsonb,
			$1, 77, 1
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, routeProviderSelectionsVersion, false); err == nil {
		t.Fatal("route provider selection Down erased durable evidence")
	}
	assertRouteProviderSelectionSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_route_provider_selections`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_route_provider_selection_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, routeProviderSelectionsVersion, false); err != nil {
		t.Fatalf("rollback empty route provider selections: %v", err)
	}
	assertRouteProviderSelectionSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, routeProviderSelectionsVersion, true); err != nil {
		t.Fatalf("reapply route provider selections: %v", err)
	}
	assertRouteProviderSelectionSchema(t, ctx, db, true)
}

func assertRouteProviderSelectionSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"extension_route_provider_selections", "extension_route_provider_selection_events",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("route provider selection table %s exists=%v want=%v", table, exists, want)
		}
	}
}
