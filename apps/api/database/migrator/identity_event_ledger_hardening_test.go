package migrator

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const identityEventLedgerHardeningVersion = int64(202607190040)

func TestIdentityEventLedgerHardeningMigrationContract(t *testing.T) {
	body, err := fs.ReadFile(migrations.Files(), "202607190040_identity_event_ledger_hardening.sql")
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.SplitN(string(body), "-- +goose Down", 2)
	if len(parts) != 2 {
		t.Fatal("identity event ledger hardening migration has no Down section")
	}
	up := strings.Join(strings.Fields(parts[0]), " ")
	for _, clause := range []string{
		"UNIQUE (link_id, next_revision)",
		"UNIQUE (user_id, field_id, next_revision)",
		"UNIQUE (selection_revision)",
		"CREATE FUNCTION enforce_identity_event_ledger_immutability()",
		"TG_OP = 'UPDATE'",
		"pg_trigger_depth() > 1",
		"OLD.actor_user_id IS NOT NULL",
		"NEW.actor_user_id IS NULL",
		"to_jsonb(NEW) - 'actor_user_id'",
		"to_jsonb(OLD) - 'actor_user_id'",
		"NOT EXISTS ( SELECT 1 FROM users WHERE id = OLD.actor_user_id )",
		"BEFORE UPDATE OR DELETE ON identity_external_link_events",
		"BEFORE TRUNCATE ON identity_external_link_events",
		"BEFORE UPDATE OR DELETE ON identity_user_field_value_events",
		"BEFORE TRUNCATE ON identity_user_field_value_events",
		"BEFORE UPDATE OR DELETE ON identity_session_policy_selection_events",
		"BEFORE TRUNCATE ON identity_session_policy_selection_events",
	} {
		if !strings.Contains(up, clause) {
			t.Fatalf("identity event ledger hardening migration missing %q", clause)
		}
	}

	down := strings.Join(strings.Fields(parts[1]), " ")
	for _, clause := range []string{
		"LOCK TABLE identity_external_link_events, identity_user_field_value_events, identity_session_policy_selection_events IN ACCESS EXCLUSIVE MODE",
		"IF EXISTS (SELECT 1 FROM identity_external_link_events) OR EXISTS (SELECT 1 FROM identity_user_field_value_events) OR EXISTS (SELECT 1 FROM identity_session_policy_selection_events)",
		"RAISE EXCEPTION 'cannot remove identity event ledger hardening while evidence exists'",
		"DROP FUNCTION IF EXISTS enforce_identity_event_ledger_immutability()",
		"DROP CONSTRAINT IF EXISTS identity_external_link_events_link_revision_key",
		"DROP CONSTRAINT IF EXISTS identity_user_field_value_events_user_field_revision_key",
		"DROP CONSTRAINT IF EXISTS identity_session_policy_selection_events_revision_key",
	} {
		if !strings.Contains(down, clause) {
			t.Fatalf("identity event ledger hardening Down missing %q", clause)
		}
	}
	for _, forbidden := range []string{"DELETE FROM", "TRUNCATE identity_"} {
		if strings.Contains(down, forbidden) {
			t.Fatalf("identity event ledger hardening Down contains %q", forbidden)
		}
	}
}

func TestIdentityEventLedgerHardeningPostgres(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)

	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := db.ExecContext(ctx, `CREATE TABLE users (id BIGSERIAL PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{202607190037, 202607190038, 202607190039, identityEventLedgerHardeningVersion} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}

	// An unused hardening migration remains reversible.
	if _, err := provider.ApplyVersion(ctx, identityEventLedgerHardeningVersion, false); err != nil {
		t.Fatalf("rollback unused identity event ledger hardening: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, identityEventLedgerHardeningVersion, true); err != nil {
		t.Fatalf("reapply identity event ledger hardening: %v", err)
	}

	var actorID int64
	if err := db.QueryRowContext(ctx, `INSERT INTO users DEFAULT VALUES RETURNING id`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_external_link_events (
			link_id, provider_id, provider_contract_version, owner_extension_id,
			action, idempotency_key, request_fingerprint, previous_revision,
			next_revision, previous_status, next_status, actor_user_id, audit_event_id
		) VALUES (
			101, 'fixture.provider', 'fixture.provider@1', 'fixture.owner',
			'link', 'external-link-1', repeat('a', 64), NULL,
			1, NULL, 'active', $1, 1001
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_user_field_value_events (
			user_id, field_id, owner_extension_id, field_contract_version,
			field_schema_digest, declaration_revision, action, previous_revision,
			next_revision, previous_value_digest, next_value_digest,
			idempotency_key, request_fingerprint, actor_user_id, audit_event_id
		) VALUES (
			202, 'fixture.field', 'fixture.owner', 'fixture.field@1',
			repeat('b', 64), 1, 'set', NULL,
			1, NULL, repeat('c', 64),
			'user-field-1', repeat('d', 64), $1, 1002
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection, actor_user_id,
			audit_event_id, selection_revision
		) VALUES (
			'select', NULL, jsonb_build_object(
			  'policyId', 'fixture.session',
			  'providerContractVersion', 'fixture.session@1',
			  'ownerExtensionId', 'fixture.owner',
			  'ownerExtensionVersionId', 1,
			  'ownerExtensionVersion', '1.0.0',
			  'ownerPackageDigest', repeat('a', 64),
			  'declarationRevision', 1
			), $1,
			1003, 1
		)
	`, actorID); err != nil {
		t.Fatal(err)
	}

	duplicateInserts := []string{
		`INSERT INTO identity_external_link_events (
			link_id, provider_id, provider_contract_version, owner_extension_id,
			action, idempotency_key, request_fingerprint, previous_revision,
			next_revision, previous_status, next_status, audit_event_id
		) VALUES (
			101, 'fixture.provider', 'fixture.provider@1', 'fixture.owner',
			'link', 'external-link-duplicate', repeat('e', 64), NULL,
			1, NULL, 'active', 2001
		)`,
		`INSERT INTO identity_user_field_value_events (
			user_id, field_id, owner_extension_id, field_contract_version,
			field_schema_digest, declaration_revision, action, previous_revision,
			next_revision, previous_value_digest, next_value_digest,
			idempotency_key, request_fingerprint, audit_event_id
		) VALUES (
			202, 'fixture.field', 'fixture.owner', 'fixture.field@1',
			repeat('b', 64), 1, 'set', NULL,
			1, NULL, repeat('f', 64),
			'user-field-duplicate', repeat('0', 64), 2002
		)`,
		`INSERT INTO identity_session_policy_selection_events (
			action, previous_selection, selected_selection, audit_event_id,
			selection_revision
		) VALUES (
			'select', NULL, jsonb_build_object(
			  'policyId', 'fixture.other',
			  'providerContractVersion', 'fixture.other@1',
			  'ownerExtensionId', 'fixture.owner',
			  'ownerExtensionVersionId', 1,
			  'ownerExtensionVersion', '1.0.0',
			  'ownerPackageDigest', repeat('b', 64),
			  'declarationRevision', 1
			), 2003, 1
		)`,
	}
	for _, query := range duplicateInserts {
		if _, err := db.ExecContext(ctx, query); err == nil || !strings.Contains(err.Error(), "duplicate key") {
			t.Fatalf("duplicate event revision error=%v", err)
		}
	}

	for _, query := range []string{
		`UPDATE identity_external_link_events SET actor_user_id = NULL WHERE link_id = 101`,
		`UPDATE identity_user_field_value_events SET audit_event_id = 9002 WHERE user_id = 202`,
		`DELETE FROM identity_session_policy_selection_events WHERE selection_revision = 1`,
		`TRUNCATE identity_external_link_events`,
		`TRUNCATE identity_user_field_value_events`,
		`TRUNCATE identity_session_policy_selection_events`,
	} {
		if _, err := db.ExecContext(ctx, query); err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("identity event mutation %q error=%v", query, err)
		}
	}

	// The FK action is the sole legitimate historical-row update.
	if _, err := db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, actorID); err != nil {
		t.Fatalf("delete event actor: %v", err)
	}
	for _, query := range []string{
		`SELECT actor_user_id IS NULL FROM identity_external_link_events WHERE link_id = 101`,
		`SELECT actor_user_id IS NULL FROM identity_user_field_value_events WHERE user_id = 202`,
		`SELECT actor_user_id IS NULL FROM identity_session_policy_selection_events WHERE selection_revision = 1`,
	} {
		var actorCleared bool
		if err := db.QueryRowContext(ctx, query).Scan(&actorCleared); err != nil {
			t.Fatal(err)
		}
		if !actorCleared {
			t.Fatalf("FK actor deletion did not clear provenance for %q", query)
		}
	}

	if _, err := provider.ApplyVersion(ctx, identityEventLedgerHardeningVersion, false); err == nil ||
		!strings.Contains(err.Error(), "cannot remove identity event ledger hardening while evidence exists") {
		t.Fatalf("rollback with identity event evidence error=%v", err)
	}
}
