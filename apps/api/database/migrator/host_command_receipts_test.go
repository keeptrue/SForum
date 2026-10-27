package migrator

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	extensionDatabaseDispositionResourcePresenceVersion = int64(202607140015)
	hostCommandReceiptsVersion                          = int64(202607140017)
)

func TestHostCommandReceiptsUpDownProtectionAndReapply(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, extensionDatabaseDispositionResourcePresenceVersion); err != nil {
		t.Fatalf("migrate isolated schema to database disposition resource presence: %v", err)
	}
	if _, err := provider.ApplyVersion(ctx, hostCommandReceiptsVersion, true); err != nil {
		t.Fatalf("apply Host Command receipts: %v", err)
	}
	assertHostCommandReceiptsSchema(t, ctx, db, true)

	var auditEventID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ('extension.host_command.committed', '{"source":"test"}'::jsonb)
		RETURNING id
	`).Scan(&auditEventID); err != nil {
		t.Fatal(err)
	}
	var trustGrantID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action,
			artifact_digests, impact_document, impact_digest
		) VALUES (
			'fixture.command', '1.0.0', repeat('a', 64), 'enable',
			'{}'::jsonb, '{"schemaVersion":"test"}'::jsonb, repeat('b', 64)
		)
		RETURNING id
	`).Scan(&trustGrantID); err != nil {
		t.Fatal(err)
	}

	insertHostCommandReceipt(t, ctx, db, trustGrantID, auditEventID)
	assertHostCommandReceipt(t, ctx, db, trustGrantID, auditEventID)
	var duplicateAuditEventID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO audit_events (action, metadata)
		VALUES ('extension.host_command.committed', '{"source":"duplicate-test"}'::jsonb)
		RETURNING id
	`).Scan(&duplicateAuditEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_host_command_receipts (
			extension_id, extension_version_id, extension_version, package_digest,
			authority_type, trust_grant_id, command_id, command_version,
			idempotency_key, request_fingerprint, result, transaction_id, audit_event_id
		) VALUES (
			'fixture.command', 99, '2.0.0', repeat('c', 64),
			'trust_grant', $1, 'core.command.fixture', '1',
			'request-1', repeat('d', 64), '{"state":"committed"}'::jsonb,
			'txn_duplicate', $2
		)
	`, trustGrantID, duplicateAuditEventID); err == nil {
		t.Fatal("exact-artifact change reopened a committed idempotency key")
	}

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_trust_grants WHERE id = $1`, trustGrantID); err == nil {
		t.Fatal("receipt allowed its exact trust grant to be deleted")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_events WHERE id = $1`, auditEventID); err != nil {
		t.Fatalf("audit retention must not be blocked by a durable receipt: %v", err)
	}
	var retainedAuditEventID int64
	if err := db.QueryRowContext(ctx, `
		SELECT audit_event_id FROM extension_host_command_receipts
		WHERE extension_id = 'fixture.command'
		  AND command_id = 'core.command.fixture'
		  AND command_version = '1'
		  AND idempotency_key = 'request-1'
	`).Scan(&retainedAuditEventID); err != nil || retainedAuditEventID != auditEventID {
		t.Fatalf("receipt lost retained audit reference: id=%d err=%v", retainedAuditEventID, err)
	}
	if _, err := provider.ApplyVersion(ctx, hostCommandReceiptsVersion, false); err == nil {
		t.Fatal("Host Command receipts Down must refuse durable evidence")
	}
	assertHostCommandReceiptsSchema(t, ctx, db, true)

	if _, err := db.ExecContext(ctx, `DELETE FROM extension_host_command_receipts`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_trust_grants WHERE id = $1`, trustGrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM audit_events WHERE id = $1`, duplicateAuditEventID); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, hostCommandReceiptsVersion, false); err != nil {
		t.Fatalf("rollback empty Host Command receipts: %v", err)
	}
	assertHostCommandReceiptsSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, hostCommandReceiptsVersion, true); err != nil {
		t.Fatalf("reapply Host Command receipts: %v", err)
	}
	assertHostCommandReceiptsSchema(t, ctx, db, true)
}

func assertHostCommandReceipt(t *testing.T, ctx context.Context, db *sql.DB, trustGrantID, auditEventID int64) {
	t.Helper()
	var (
		extensionVersionID  int64
		extensionVersion    string
		packageDigest       string
		fingerprint         string
		transactionID       string
		storedTrustGrantID  int64
		storedAuditEventID  int64
		resultSchemaID      string
		resultSchemaVersion string
		resultOK            bool
	)
	if err := db.QueryRowContext(ctx, `
		SELECT extension_version_id, extension_version, package_digest,
		       request_fingerprint, transaction_id, trust_grant_id, audit_event_id,
		       result #>> '{output,schemaId}', result #>> '{output,schemaVersion}',
		       (result #>> '{output,value,ok}')::boolean
		FROM extension_host_command_receipts
		WHERE extension_id = 'fixture.command'
		  AND command_id = 'core.command.fixture'
		  AND command_version = '1'
		  AND idempotency_key = 'request-1'
	`).Scan(
		&extensionVersionID, &extensionVersion, &packageDigest,
		&fingerprint, &transactionID, &storedTrustGrantID, &storedAuditEventID,
		&resultSchemaID, &resultSchemaVersion, &resultOK,
	); err != nil {
		t.Fatal(err)
	}
	if extensionVersionID != 42 || extensionVersion != "1.0.0" || packageDigest != strings.Repeat("a", 64) ||
		fingerprint != strings.Repeat("c", 64) || transactionID != "txn_fixture" ||
		storedTrustGrantID != trustGrantID || storedAuditEventID != auditEventID ||
		resultSchemaID != "fixture.result" || resultSchemaVersion != "1" || !resultOK {
		t.Fatalf("unexpected Host Command receipt snapshot")
	}
}

func insertHostCommandReceipt(t *testing.T, ctx context.Context, db *sql.DB, trustGrantID, auditEventID int64) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_host_command_receipts (
			extension_id, extension_version_id, extension_version, package_digest,
			authority_type, trust_grant_id, command_id, command_version,
			idempotency_key, request_fingerprint, result, transaction_id, audit_event_id
		) VALUES (
			'fixture.command', 42, '1.0.0', repeat('a', 64),
			'trust_grant', $1, 'core.command.fixture', '1',
			'request-1', repeat('c', 64),
			'{"state":"COMMAND_STATE_COMMITTED","output":{"schemaId":"fixture.result","schemaVersion":"1","value":{"ok":true}}}'::jsonb,
			'txn_fixture', $2
		)
	`, trustGrantID, auditEventID); err != nil {
		t.Fatal(err)
	}
}

func assertHostCommandReceiptsSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	var exists bool
	if err := db.QueryRowContext(ctx, `
		SELECT to_regclass(current_schema() || '.extension_host_command_receipts') IS NOT NULL
	`).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists != want {
		t.Fatalf("Host Command receipts table exists=%v, want %v", exists, want)
	}
}
