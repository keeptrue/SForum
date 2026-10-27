package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const providerSlotSelectionsVersion = int64(202607150023)

func TestProviderSlotSelectionsUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, extensionDatabaseDispositionResourcePresenceVersion); err != nil {
		t.Fatalf("migrate isolated schema to database disposition presence: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, providerSlotSelectionsVersion, true); err != nil {
		t.Fatalf("apply provider slot selections: %v", err)
	}
	assertProviderSlotSelectionSchema(t, ctx, db, true)

	var actorID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO users (username, username_lower, email, email_lower, display_name)
		VALUES (
		  'provider-selector', 'provider-selector',
		  'provider-selector@example.test', 'provider-selector@example.test', 'Provider Selector'
		) RETURNING id
	`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_provider_slot_selections (
			contract_id, contract_version, slot,
			contract_extension_id, contract_extension_version_id,
			contract_extension_version, contract_package_digest,
			candidate_id, provider_extension_id, provider_extension_version_id,
			provider_extension_version, provider_package_digest,
			selected_by_user_id, selection_audit_event_id
		) VALUES (
			'fixture.delivery', 'fixture.delivery@1', 'fixture.delivery.provider',
			'fixture.owner', 41, '1.0.0', repeat('a', 64),
			'fixture.delivery.primary', 'fixture.provider', 42, '1.0.0', repeat('b', 64),
			$1, 77
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_provider_slot_selection_events (
			contract_id, contract_version, slot, action, selected_selection,
			actor_user_id, audit_event_id, selection_revision
		) VALUES (
			'fixture.delivery', 'fixture.delivery@1', 'fixture.delivery.provider', 'select',
			'{"providerExtensionId":"fixture.provider","packageDigest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}'::jsonb,
			$1, 77, 1
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, providerSlotSelectionsVersion, false); err == nil {
		t.Fatal("provider slot selection Down erased durable evidence")
	}
	assertProviderSlotSelectionSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_provider_slot_selections`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_provider_slot_selection_events`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, providerSlotSelectionsVersion, false); err != nil {
		t.Fatalf("rollback empty provider slot selections: %v", err)
	}
	assertProviderSlotSelectionSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, providerSlotSelectionsVersion, true); err != nil {
		t.Fatalf("reapply provider slot selections: %v", err)
	}
	assertProviderSlotSelectionSchema(t, ctx, db, true)
}

func assertProviderSlotSelectionSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{
		"extension_provider_slot_selections", "extension_provider_slot_selection_events",
	} {
		var exists bool
		if err := db.QueryRowContext(ctx, `SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("provider slot selection table %s exists=%v want=%v", table, exists, want)
		}
	}
}
