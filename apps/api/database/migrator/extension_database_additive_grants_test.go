package migrator

import (
	"context"
	"database/sql"
	"testing"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const extensionDatabaseAdditiveGrantsVersion = int64(202607150022)

func TestExtensionDatabaseAdditiveGrantsMigrationBackfillAndRuntimeOverlap(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	db, provider := openIsolatedLifecycleLeaseMigrationDB(t, ctx, databaseURL)
	if _, err := provider.UpTo(ctx, lifecycleMigrationProofsVersion); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseRegistryVersion, true); err != nil {
		t.Fatal(err)
	}

	const extensionID = "fixture.additive.database"
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_resources (
			extension_id, schema_name, owner_role_name, runtime_role_name
		) VALUES (
			$1, 'sforum_ext_s_additive_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_o_additive_aaaaaaaaaaaaaaaaaaaa',
			'sforum_ext_r_additive_aaaaaaaaaaaaaaaaaaaa'
		)
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	var sourceGrantID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_database_grants (
			extension_id, extension_version_id, extension_version, package_digest,
			database_contract_version, authority, granted_by_user_id, grant_audit_event_id
		) VALUES ($1, 101, '1.0.0', repeat('a', 64), $2, 'raw_core', 42, 91)
		RETURNING id
	`, extensionID, extensionID+".database@1").Scan(&sourceGrantID); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.ApplyVersion(ctx, extensionDatabaseAdditiveGrantsVersion, true); err != nil {
		t.Fatal(err)
	}
	var powerCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_database_grant_powers WHERE grant_id = $1
	`, sourceGrantID).Scan(&powerCount); err != nil {
		t.Fatal(err)
	}
	if powerCount != 4 {
		t.Fatalf("raw_core legacy power count = %d, want 4", powerCount)
	}

	var targetGrantID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_database_grants (
			extension_id, extension_version_id, extension_version, package_digest,
			database_contract_version, authority, granted_by_user_id, grant_audit_event_id
		) VALUES ($1, 202, '2.0.0', repeat('b', 64), $2, 'additive', 42, 92)
		RETURNING id
	`, extensionID, extensionID+".database@2").Scan(&targetGrantID); err != nil {
		t.Fatalf("source and target exact grants did not coexist: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_grant_powers (grant_id, power, source, ordinal)
		VALUES ($1, 'own_schema', 'manifest_grants', 1),
		       ($1, 'raw_core', 'manifest_grants', 4)
	`, targetGrantID); err != nil {
		t.Fatal(err)
	}

	insertRuntimeLease(t, ctx, db, sourceGrantID, extensionID, 101, "1.0.0", "a", "source-runtime", "1", "source")
	insertRuntimeLease(t, ctx, db, targetGrantID, extensionID, 202, "2.0.0", "b", "target-runtime", "2", "target")
	var liveLeases int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM extension_database_runtime_leases
		WHERE extension_id = $1 AND status = 'active'
	`, extensionID).Scan(&liveLeases); err != nil {
		t.Fatal(err)
	}
	if liveLeases != 2 {
		t.Fatalf("live runtime leases = %d, want source and target", liveLeases)
	}

	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_runtime_leases (
			lease_id, grant_id, extension_id, extension_version_id,
			extension_version, package_digest, runtime_instance_id, role_name,
			credential_fingerprint, issued_by, issue_audit_event_id, lease_expires_at
		) VALUES (
			repeat('3', 64), $1, $2, 202,
			'2.0.0', repeat('b', 64), 'forged-runtime',
			'sforum_ext_l_forged_aaaaaaaaaaaaaaaaaaaa', repeat('c', 64),
			'host', 103, statement_timestamp() + interval '10 minutes'
		)
	`, sourceGrantID, extensionID); err == nil {
		t.Fatal("runtime lease accepted an artifact tuple from another grant")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_runtime_leases (
			lease_id, grant_id, extension_id, extension_version_id,
			extension_version, package_digest, runtime_instance_id, role_name,
			credential_fingerprint, issued_by, issue_audit_event_id, lease_expires_at
		) VALUES (
			repeat('4', 64), $1, $2, 101,
			'1.0.0', repeat('a', 64), 'source-runtime',
			'sforum_ext_l_duplicate_aaaaaaaaaaaaaaaaa', repeat('d', 64),
			'host', 104, statement_timestamp() + interval '10 minutes'
		)
	`, sourceGrantID, extensionID); err == nil {
		t.Fatal("runtime instance received two live database leases")
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_runtime_leases (
			lease_id, grant_id, extension_id, extension_version_id,
			extension_version, package_digest, runtime_instance_id, role_name,
			credential_fingerprint, issued_by, issue_audit_event_id, lease_expires_at
		) VALUES (
			repeat('5', 64), $1, $2, 101,
			'1.0.0', repeat('a', 64), 'invalid-actor-runtime',
			'sforum_ext_l_actor_aaaaaaaaaaaaaaaaaaaaa', repeat('e', 64),
			'actor', 105, statement_timestamp() + interval '10 minutes'
		)
	`, sourceGrantID, extensionID); err == nil {
		t.Fatal("actor-issued runtime lease without an actor was accepted")
	}

	if _, err := provider.ApplyVersion(ctx, extensionDatabaseAdditiveGrantsVersion, false); err == nil {
		t.Fatal("additive database authority evidence was removed by Down")
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_runtime_leases`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_grant_powers`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_credentials`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_grants`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM extension_database_resources`); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseAdditiveGrantsVersion, false); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseAdditiveSchema(t, ctx, db, false)
	if _, err := provider.ApplyVersion(ctx, extensionDatabaseAdditiveGrantsVersion, true); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseAdditiveSchema(t, ctx, db, true)
}

func insertRuntimeLease(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	grantID int64,
	extensionID string,
	versionID int64,
	version, digestByte, instanceID, leaseByte, roleSuffix string,
) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_runtime_leases (
			lease_id, grant_id, extension_id, extension_version_id,
			extension_version, package_digest, runtime_instance_id, role_name,
			credential_fingerprint, issued_by, issue_audit_event_id, lease_expires_at
		) VALUES (
			repeat($1, 64), $2, $3, $4,
			$5, repeat($6, 64), $7, $8,
			repeat('f', 64), 'host', $9, statement_timestamp() + interval '10 minutes'
		)
	`, leaseByte, grantID, extensionID, versionID, version, digestByte, instanceID,
		"sforum_ext_l_"+roleSuffix+"_aaaaaaaaaaaaaaaaaaaa", 100+versionID); err != nil {
		t.Fatal(err)
	}
}

func assertExtensionDatabaseAdditiveSchema(t *testing.T, ctx context.Context, db *sql.DB, want bool) {
	t.Helper()
	for _, table := range []string{"extension_database_grant_powers", "extension_database_runtime_leases"} {
		var exists bool
		if err := db.QueryRowContext(ctx, `
			SELECT to_regclass(current_schema() || '.' || $1) IS NOT NULL
		`, table).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Fatalf("table %s exists=%t want=%t", table, exists, want)
		}
	}
}
