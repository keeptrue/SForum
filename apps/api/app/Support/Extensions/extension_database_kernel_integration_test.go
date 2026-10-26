package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func TestPostgresExtensionDatabaseKernelAuthorityUsesExactCoreOwnerMembership(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	extensionID := fmt.Sprintf("p5.kernel.authority.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "kernel-authority", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantRawCore,
			extensionmanifest.DatabaseGrantKernel,
		},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(
		ctx,
		ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: "kernel-runtime",
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 901, AuditEventID: 902,
			},
		},
	)
	if err != nil {
		t.Fatalf("issue kernel lease: %v", err)
	}
	heartbeat, err := registry.HeartbeatRuntimeLease(
		ctx, extensionDatabaseRuntimeCredentialRef(credential), credential.Revision,
	)
	if err != nil {
		t.Fatalf("heartbeat kernel lease: %v", err)
	}
	if heartbeat.Revision != credential.Revision+1 || !heartbeat.ExpiresAt.After(credential.ExpiresAt) {
		t.Fatalf("kernel heartbeat = revision:%d expiry:%s", heartbeat.Revision, heartbeat.ExpiresAt)
	}
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	coreOwner, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	var adminOption, inheritOption, setOption bool
	if err := pool.QueryRow(ctx, `
		SELECT memberships.admin_option, memberships.inherit_option, memberships.set_option
		FROM pg_auth_members AS memberships
		JOIN pg_roles AS granted ON granted.oid = memberships.roleid
		JOIN pg_roles AS member ON member.oid = memberships.member
		WHERE granted.rolname = $1 AND member.rolname = $2
	`, coreOwner, credential.RoleName).Scan(&adminOption, &inheritOption, &setOption); err != nil {
		t.Fatalf("inspect kernel Core owner membership: %v", err)
	}
	if adminOption || !inheritOption || setOption {
		t.Fatalf("kernel Core membership options = admin:%t inherit:%t set:%t", adminOption, inheritOption, setOption)
	}
	var canSetCore, canCreateSchema bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_has_role($1, $2, 'SET'),
		       has_database_privilege($1, current_database(), 'CREATE')
	`, credential.RoleName, coreOwner).Scan(&canSetCore, &canCreateSchema); err != nil {
		t.Fatal(err)
	}
	if canSetCore || canCreateSchema {
		t.Fatalf("kernel effective escalation = setCore:%t databaseCreate:%t", canSetCore, canCreateSchema)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if err := validateExtensionDatabaseKernelLeaseAuthority(ctx, tx, credential.RoleName, coreOwner); err != nil {
		t.Fatalf("validate exact kernel direct ACL isolation: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}

	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	execExtensionDatabaseInsufficientPrivilege(
		t, ctx, connection, `SET ROLE `+pgx.Identifier{coreOwner}.Sanitize(),
	)
	forbiddenSchema := pgx.Identifier{fmt.Sprintf("p5_kernel_forbidden_%d", time.Now().UnixNano())}.Sanitize()
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `CREATE SCHEMA `+forbiddenSchema)
	coreTableName := fmt.Sprintf("p5_kernel_core_%d", time.Now().UnixNano())
	coreTable := pgx.Identifier{extensionDatabaseCoreSchema, coreTableName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP TABLE IF EXISTS `+coreTable+` CASCADE`)
	})
	execExtensionDatabaseKernelDDL(t, ctx, connection, `CREATE TABLE `+coreTable+` (id BIGINT PRIMARY KEY)`)
	var owner string
	if err := pool.QueryRow(ctx, `
		SELECT owners.rolname
		FROM pg_class AS classes
		JOIN pg_namespace AS namespaces ON namespaces.oid = classes.relnamespace
		JOIN pg_roles AS owners ON owners.oid = classes.relowner
		WHERE namespaces.nspname = $1 AND classes.relname = $2
	`, extensionDatabaseCoreSchema, coreTableName).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner != credential.RoleName {
		t.Fatalf("kernel-created Core table owner = %s, want %s", owner, credential.RoleName)
	}
}

func TestPostgresExtensionDatabaseKernelSourceRevokePreservesTargetAndRawCore(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	nonce := time.Now().UnixNano()
	extensionID := fmt.Sprintf("p5.kernel.overlap.%d", nonce)
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "kernel-overlap", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantKernel,
		},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	issue := func(runtimeID string, actor, audit int64) ExtensionDatabaseRuntimeCredential {
		t.Helper()
		credential, issueErr := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: runtimeID,
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: actor, AuditEventID: audit,
			},
		})
		if issueErr != nil {
			t.Fatalf("issue %s kernel lease: %v", runtimeID, issueErr)
		}
		return credential
	}
	source := issue("source-runtime", 910, 911)
	sourceConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, source)
	if err != nil {
		t.Fatal(err)
	}
	defer sourceConnection.Close(context.Background())

	sourceTableName := fmt.Sprintf("p5_kernel_source_%d", nonce)
	sourceSequenceName := sourceTableName + "_id_seq"
	sourceRoutineName := fmt.Sprintf("p5_kernel_source_fn_%d", nonce)
	sourceTypeName := fmt.Sprintf("p5_kernel_source_state_%d", nonce)
	targetTableName := fmt.Sprintf("p5_kernel_target_%d", nonce)
	targetRoutineName := fmt.Sprintf("p5_kernel_target_fn_%d", nonce)
	sourceOwnTableName := fmt.Sprintf("p5_kernel_own_%d", nonce)
	sourceTable := pgx.Identifier{extensionDatabaseCoreSchema, sourceTableName}.Sanitize()
	sourceRoutine := pgx.Identifier{extensionDatabaseCoreSchema, sourceRoutineName}.Sanitize()
	sourceType := pgx.Identifier{extensionDatabaseCoreSchema, sourceTypeName}.Sanitize()
	targetTable := pgx.Identifier{extensionDatabaseCoreSchema, targetTableName}.Sanitize()
	targetRoutine := pgx.Identifier{extensionDatabaseCoreSchema, targetRoutineName}.Sanitize()
	sourceOwnTable := pgx.Identifier{identifiers.Schema, sourceOwnTableName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool,
			`DROP FUNCTION IF EXISTS `+sourceRoutine+`(); `+
				`DROP FUNCTION IF EXISTS `+targetRoutine+`(); `+
				`DROP TABLE IF EXISTS `+sourceTable+`, `+targetTable+` CASCADE; `+
				`DROP TYPE IF EXISTS `+sourceType+` CASCADE`,
		)
	})
	execExtensionDatabaseKernelDDL(t, ctx, sourceConnection, []string{
		`CREATE TYPE ` + sourceType + ` AS ENUM ('ready')`,
		`CREATE TABLE ` + sourceTable + ` (id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, note TEXT NOT NULL)`,
		`CREATE FUNCTION ` + sourceRoutine + `() RETURNS TEXT LANGUAGE SQL AS 'SELECT ''source'''`,
		`CREATE TABLE ` + sourceOwnTable + ` (id BIGINT PRIMARY KEY)`,
	}...)
	var publicCanExecute bool
	if err := pool.QueryRow(ctx, `
		SELECT has_function_privilege('public', routines.oid, 'EXECUTE')
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		WHERE namespaces.nspname = $1 AND routines.proname = $2
	`, extensionDatabaseCoreSchema, sourceRoutineName).Scan(&publicCanExecute); err != nil {
		t.Fatal(err)
	}
	if publicCanExecute {
		t.Fatal("kernel-created Core routine retained default PUBLIC execution")
	}

	target := issue("target-runtime", 912, 913)
	targetConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, target)
	if err != nil {
		t.Fatal(err)
	}
	defer targetConnection.Close(context.Background())
	execExtensionDatabaseKernelDDL(t, ctx, targetConnection,
		`CREATE TABLE `+targetTable+` (id BIGINT PRIMARY KEY, note TEXT NOT NULL)`,
		`CREATE FUNCTION `+targetRoutine+`() RETURNS TEXT LANGUAGE SQL AS 'SELECT ''target'''`,
	)
	lowerExtensionID := fmt.Sprintf("p5.kernel.lower-overlap.%d", nonce)
	lowerArtifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, lowerExtensionID, "1.0.0", "kernel-lower-overlap",
		[]string{extensionmanifest.DatabaseGrantCoreViews},
	)
	lowerIdentifiers, err := ExtensionDatabaseIdentifiersFor(lowerExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, lowerExtensionID, lowerIdentifiers)
	})
	lowerCredential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: lowerArtifact, RuntimeInstanceID: "lower-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 922, AuditEventID: 923,
		},
	})
	if err != nil {
		t.Fatalf("issue lower-tier lease during kernel overlap: %v", err)
	}
	lowerConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, lowerCredential)
	if err != nil {
		t.Fatal(err)
	}
	defer lowerConnection.Close(context.Background())
	execExtensionDatabaseInsufficientPrivilege(t, ctx, lowerConnection, `SELECT * FROM `+sourceTable)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, lowerConnection, `SELECT `+sourceRoutine+`()`)

	rawExtensionID := fmt.Sprintf("p5.kernel.raw-overlap.%d", nonce)
	rawArtifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, rawExtensionID, "1.0.0", "kernel-raw-overlap", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantRawCore,
		},
	)
	rawIdentifiers, err := ExtensionDatabaseIdentifiersFor(rawExtensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, rawExtensionID, rawIdentifiers)
	})
	rawCredential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: rawArtifact, RuntimeInstanceID: "raw-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 914, AuditEventID: 915,
		},
	})
	if err != nil {
		t.Fatalf("issue raw Core lease during kernel overlap: %v", err)
	}
	rawConnection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, rawCredential)
	if err != nil {
		t.Fatal(err)
	}
	defer rawConnection.Close(context.Background())
	for index, table := range []string{sourceTable, targetTable} {
		if _, err := rawConnection.Exec(ctx,
			`INSERT INTO `+table+` (id, note) VALUES ($1, 'raw')`, int64(index+1),
		); err != nil {
			t.Fatalf("raw Core writes kernel table before source revoke: %v", err)
		}
	}

	sourceRef := extensionDatabaseRuntimeCredentialRef(source)
	drained, err := registry.BeginRuntimeLeaseDrain(ctx, sourceRef, source.Revision)
	if err != nil {
		t.Fatalf("drain source kernel lease: %v", err)
	}
	if drained.Status != ExtensionDatabaseLeaseDraining {
		t.Fatalf("source drain status = %s", drained.Status)
	}
	revoked, err := registry.RevokeRuntimeLease(ctx, sourceRef, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 916, AuditEventID: 917,
	})
	if err != nil {
		t.Fatalf("revoke source kernel lease: %v", err)
	}
	if revoked.Status != ExtensionDatabaseLeaseRevoked {
		t.Fatalf("source revoke status = %s", revoked.Status)
	}

	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	coreOwner, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "relation", extensionDatabaseCoreSchema, sourceTableName, coreOwner)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "relation", extensionDatabaseCoreSchema, sourceSequenceName, coreOwner)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "routine", extensionDatabaseCoreSchema, sourceRoutineName, coreOwner)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "type", extensionDatabaseCoreSchema, sourceTypeName, coreOwner)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "relation", identifiers.Schema, sourceOwnTableName, identifiers.OwnerRole)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "relation", extensionDatabaseCoreSchema, targetTableName, target.RoleName)
	assertExtensionDatabaseKernelObjectOwner(t, ctx, pool, "routine", extensionDatabaseCoreSchema, targetRoutineName, target.RoleName)
	var sourceRoleExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, source.RoleName,
	).Scan(&sourceRoleExists); err != nil {
		t.Fatal(err)
	}
	if sourceRoleExists {
		t.Fatal("source kernel role survived exact revoke")
	}

	execExtensionDatabaseKernelDDL(t, ctx, targetConnection, `ALTER TABLE `+targetTable+` ADD COLUMN target_only TEXT`)
	var targetRoutineResult string
	if err := targetConnection.QueryRow(ctx, `SELECT `+targetRoutine+`()`).Scan(&targetRoutineResult); err != nil || targetRoutineResult != "target" {
		t.Fatalf("target kernel routine after source revoke = %q, %v", targetRoutineResult, err)
	}
	for _, table := range []string{sourceTable, targetTable} {
		var count int
		if err := rawConnection.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("raw Core access after source revoke for %s: count=%d err=%v", table, count, err)
		}
	}

	if _, err := registry.RevokeRuntimeLease(ctx, extensionDatabaseRuntimeCredentialRef(rawCredential), ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 918, AuditEventID: 919,
	}); err != nil {
		t.Fatalf("revoke overlap raw Core lease: %v", err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, extensionDatabaseRuntimeCredentialRef(lowerCredential), ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 924, AuditEventID: 925,
	}); err != nil {
		t.Fatalf("revoke overlap lower-tier lease: %v", err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, extensionDatabaseRuntimeCredentialRef(target), ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 920, AuditEventID: 921,
	}); err != nil {
		t.Fatalf("revoke target kernel lease: %v", err)
	}
}

func TestPostgresExtensionDatabaseKernelRevokeFailsClosedForUnsupportedCoreObject(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	nonce := time.Now().UnixNano()
	extensionID := fmt.Sprintf("p5.kernel.unsupported.%d", nonce)
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "kernel-unsupported",
		[]string{extensionmanifest.DatabaseGrantKernel},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "unsupported-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 930, AuditEventID: 931,
		},
	})
	if err != nil {
		t.Fatalf("issue unsupported-object kernel lease: %v", err)
	}
	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	tableName := fmt.Sprintf("p5_kernel_supported_%d", nonce)
	collationName := fmt.Sprintf("p5_kernel_collation_%d", nonce)
	table := pgx.Identifier{extensionDatabaseCoreSchema, tableName}.Sanitize()
	collation := pgx.Identifier{extensionDatabaseCoreSchema, collationName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool,
			`DROP COLLATION IF EXISTS `+collation+`; DROP TABLE IF EXISTS `+table+` CASCADE`,
		)
	})
	setupTx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer setupTx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, setupTx); err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(ctx, `CREATE TABLE `+table+` (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatalf("create supported kernel table: %v", err)
	}
	if _, err := setupTx.Exec(ctx,
		`CREATE COLLATION `+collation+` (provider = libc, locale = 'C')`,
	); err != nil {
		t.Fatalf("create unsupported kernel collation: %v", err)
	}
	var largeObjectOID uint32
	if err := setupTx.QueryRow(ctx, `SELECT lo_create(0)`).Scan(&largeObjectOID); err != nil {
		t.Fatalf("create schema-less kernel large object: %v", err)
	}
	if err := setupTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, fmt.Sprintf(
			`SELECT lo_unlink(%d) FROM pg_largeobject_metadata WHERE oid = %d`, largeObjectOID, largeObjectOID,
		))
	})

	ref := extensionDatabaseRuntimeCredentialRef(credential)
	pending, err := registry.RevokeRuntimeLease(ctx, ref, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerHost,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("unsupported Core object revoke error = %v", err)
	}
	if pending.Status != ExtensionDatabaseLeaseFailed ||
		pending.FailureCode != extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode ||
		pending.RevokedAt == nil {
		t.Fatalf("unsupported Core object pending fence = %#v", pending)
	}
	lease, err := registry.InspectRuntimeLease(ctx, ref)
	if err != nil || lease.Status != ExtensionDatabaseLeaseFailed ||
		lease.FailureCode != extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode {
		t.Fatalf("failed revoke lease state = %s, %v", lease.Status, err)
	}
	if _, err := connection.Exec(context.Background(), `SELECT 1`); err == nil {
		t.Fatal("cleanup-pending kernel session survived authority fencing")
	}
	if reconnected, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential); err == nil {
		reconnected.Close(context.Background())
		t.Fatal("cleanup-pending kernel credential reconnected")
	}
	if _, err := registry.HeartbeatRuntimeLease(ctx, ref, lease.Revision); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("cleanup-pending kernel lease accepted heartbeat: %v", err)
	}
	if _, err := registry.BeginRuntimeLeaseDrain(ctx, ref, lease.Revision); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseConflict) {
		t.Fatalf("cleanup-pending kernel lease accepted drain: %v", err)
	}
	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "unsupported-runtime-restart",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerHost,
		},
	}); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("cleanup-pending extension issued replacement lease: %v", err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", extensionDatabaseCoreSchema, tableName, credential.RoleName,
	)
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "collation", extensionDatabaseCoreSchema, collationName, credential.RoleName,
	)
	var roleExists, canLogin, expiresAtEpoch bool
	var revokeAuditCount int
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
		        COALESCE((SELECT rolcanlogin FROM pg_roles WHERE rolname = $1), FALSE),
		        COALESCE((SELECT rolvaliduntil = 'epoch'::timestamptz FROM pg_roles WHERE rolname = $1), FALSE),
		        (SELECT count(*)
		         FROM extension_database_runtime_leases AS leases
		         JOIN audit_events AS events ON events.id = leases.revoke_audit_event_id
		         WHERE leases.lease_id = $2 AND events.action = $3)`,
		credential.RoleName, credential.LeaseID, extensionDatabaseRuntimeLeaseRevokedAudit,
	).Scan(&roleExists, &canLogin, &expiresAtEpoch, &revokeAuditCount); err != nil {
		t.Fatal(err)
	}
	if !roleExists || canLogin || !expiresAtEpoch || revokeAuditCount != 1 {
		t.Fatalf(
			"cleanup-pending kernel fence = role:%t login:%t epoch:%t audits:%d",
			roleExists, canLogin, expiresAtEpoch, revokeAuditCount,
		)
	}

	execExtensionDatabaseTestPhysicalMutation(t, ctx, pool, `DROP COLLATION `+collation)
	pending, err = registry.RevokeRuntimeLease(ctx, ref, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 934, AuditEventID: 935,
	})
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) ||
		pending.FailureCode != extensionDatabaseRuntimeLeaseCleanupPendingRevokeCode {
		t.Fatalf("schema-less Core-adjacent object revoke error = %v", err)
	}
	execExtensionDatabaseTestPhysicalMutation(t, ctx, pool, fmt.Sprintf(`SELECT lo_unlink(%d)`, largeObjectOID))
	revoked, err := registry.RevokeRuntimeLease(ctx, ref, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 936, AuditEventID: 937,
	})
	if err != nil {
		t.Fatalf("retry kernel revoke after unsupported object removal: %v", err)
	}
	if revoked.Status != ExtensionDatabaseLeaseRevoked || revoked.FailureCode != "" {
		t.Fatalf("completed unsupported-object cleanup = %#v", revoked)
	}
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	coreOwner, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", extensionDatabaseCoreSchema, tableName, coreOwner,
	)
}

func TestPostgresExtensionDatabaseKernelReaperUsesExactOwnershipCleanup(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	nonce := time.Now().UnixNano()
	extensionID := fmt.Sprintf("p5.kernel.reaper.%d", nonce)
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "kernel-reaper", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantKernel,
		},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "reaper-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 940, AuditEventID: 941,
		},
	})
	if err != nil {
		t.Fatalf("issue kernel reaper lease: %v", err)
	}
	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	coreTableName := fmt.Sprintf("p5_kernel_reaper_%d", nonce)
	ownTableName := fmt.Sprintf("p5_kernel_reaper_own_%d", nonce)
	coreTable := pgx.Identifier{extensionDatabaseCoreSchema, coreTableName}.Sanitize()
	ownTable := pgx.Identifier{identifiers.Schema, ownTableName}.Sanitize()
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP TABLE IF EXISTS `+coreTable+` CASCADE`)
	})
	execExtensionDatabaseKernelDDL(t, ctx, connection, []string{
		`CREATE TABLE ` + coreTable + ` (id BIGINT PRIMARY KEY)`,
		`CREATE TABLE ` + ownTable + ` (id BIGINT PRIMARY KEY)`,
	}...)
	expiryTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer expiryTx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, expiryTx); err != nil {
		t.Fatal(err)
	}
	var leaseExpiresAt time.Time
	if err := expiryTx.QueryRow(ctx, `
		UPDATE extension_database_runtime_leases
		SET issued_at = statement_timestamp() - interval '4 minutes',
		    last_heartbeat_at = statement_timestamp() - interval '2 minutes',
		    lease_expires_at = statement_timestamp() - interval '1 minute'
		WHERE lease_id = $1
		RETURNING lease_expires_at
	`, credential.LeaseID).Scan(&leaseExpiresAt); err != nil {
		t.Fatal(err)
	}
	if _, err := expiryTx.Exec(ctx,
		`ALTER ROLE `+pgx.Identifier{credential.RoleName}.Sanitize()+` VALID UNTIL '`+
			leaseExpiresAt.UTC().Format(time.RFC3339Nano)+`'`,
	); err != nil {
		t.Fatal(err)
	}
	if err := expiryTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	count, err := registry.reapExpiredRuntimeLeasesForExtension(
		ctx, extensionID, DefaultExtensionDatabaseRuntimeLeaseReapLimit,
	)
	if err != nil || count != 1 {
		t.Fatalf("reap kernel lease = %d, %v", count, err)
	}
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	coreOwner, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", extensionDatabaseCoreSchema, coreTableName, coreOwner,
	)
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, pool, "relation", identifiers.Schema, ownTableName, identifiers.OwnerRole,
	)
	snapshot, err := registry.InspectRuntimeLease(ctx, extensionDatabaseRuntimeCredentialRef(credential))
	if err != nil || snapshot.Status != ExtensionDatabaseLeaseFailed ||
		snapshot.FailureCode != extensionDatabaseRuntimeLeaseExpiredCode {
		t.Fatalf("kernel reaper snapshot = %#v, %v", snapshot, err)
	}
	var roleExists bool
	if err := pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, credential.RoleName,
	).Scan(&roleExists); err != nil {
		t.Fatal(err)
	}
	if roleExists {
		t.Fatal("kernel role survived reaper ownership cleanup")
	}
}

func TestPostgresExtensionDatabaseKernelRejectsForgedCoreOwnerIdentity(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	nonce := time.Now().UnixNano()
	extensionID := fmt.Sprintf("p5.kernel.forged.%d", nonce)
	forgedRoleName, err := ExtensionDatabaseRuntimeLeaseRoleFor(
		extensionID, "forged-runtime", strings.Repeat("f", 64),
	)
	if err != nil {
		t.Fatal(err)
	}
	forgedRole := pgx.Identifier{forgedRoleName}.Sanitize()
	tableName := fmt.Sprintf("p5_kernel_forged_%d", nonce)
	table := pgx.Identifier{extensionDatabaseCoreSchema, tableName}.Sanitize()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE ROLE `+forgedRole+` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE `+table+` (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE `+table+` OWNER TO `+forgedRole); err != nil {
		t.Fatal(err)
	}
	if _, _, err := validateExtensionDatabaseKernelCoreState(ctx, tx); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("forged Core owner validation error = %v", err)
	}
	assertExtensionDatabaseKernelObjectOwner(
		t, ctx, tx, "relation", extensionDatabaseCoreSchema, tableName, forgedRoleName,
	)
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresExtensionDatabaseKernelRejectsCoreOwnerAndMembershipDrift(t *testing.T) {
	t.Run("Core owner database CREATE", func(t *testing.T) {
		ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
		nonce := time.Now().UnixNano()
		extensionID := fmt.Sprintf("p5.kernel.owner-drift.%d", nonce)
		artifact := insertExtensionDatabaseRuntimeLeaseFixture(
			t, ctx, pool, extensionID, "1.0.0", "kernel-owner-drift",
			[]string{extensionmanifest.DatabaseGrantKernel},
		)
		identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
		var databaseName string
		if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
			t.Fatal(err)
		}
		coreOwner, err := coreauthority.OwnerRoleName(databaseName)
		if err != nil {
			t.Fatal(err)
		}
		database := pgx.Identifier{databaseName}.Sanitize()
		owner := pgx.Identifier{coreOwner}.Sanitize()
		driftTx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer driftTx.Rollback(ctx)
		if err := lockExtensionDatabasePhysicalAuthority(ctx, driftTx); err != nil {
			t.Fatal(err)
		}
		if _, err := driftTx.Exec(ctx, `GRANT CREATE ON DATABASE `+database+` TO `+owner); err != nil {
			t.Fatal(err)
		}
		if _, _, err := validateExtensionDatabaseKernelCoreState(ctx, driftTx); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
			t.Fatalf("Core owner CREATE drift validation error = %v", err)
		}
		if err := driftTx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
		request := ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: "owner-drift-runtime",
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 952, AuditEventID: 953,
			},
		}
		credential, err := registry.IssueRuntimeLease(ctx, request)
		if err != nil {
			t.Fatalf("issue after Core owner drift restoration: %v", err)
		}
		if _, err := registry.RevokeRuntimeLease(
			ctx, extensionDatabaseRuntimeCredentialRef(credential),
			ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 954, AuditEventID: 955,
			},
		); err != nil {
			t.Fatalf("revoke restored Core owner lease: %v", err)
		}
	})

	t.Run("live kernel SET membership", func(t *testing.T) {
		ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
		nonce := time.Now().UnixNano()
		extensionID := fmt.Sprintf("p5.kernel.membership-drift.%d", nonce)
		artifact := insertExtensionDatabaseRuntimeLeaseFixture(
			t, ctx, pool, extensionID, "1.0.0", "kernel-membership-drift",
			[]string{extensionmanifest.DatabaseGrantKernel},
		)
		identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
		registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
		sourceRequest := ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: "membership-source",
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 956, AuditEventID: 957,
			},
		}
		source, err := registry.IssueRuntimeLease(ctx, sourceRequest)
		if err != nil {
			t.Fatalf("issue membership drift source: %v", err)
		}
		var databaseName string
		if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
			t.Fatal(err)
		}
		coreOwner, err := coreauthority.OwnerRoleName(databaseName)
		if err != nil {
			t.Fatal(err)
		}
		owner := pgx.Identifier{coreOwner}.Sanitize()
		role := pgx.Identifier{source.RoleName}.Sanitize()
		expiryDrift, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer expiryDrift.Rollback(ctx)
		if err := lockExtensionDatabasePhysicalAuthority(ctx, expiryDrift); err != nil {
			t.Fatal(err)
		}
		if _, err := expiryDrift.Exec(ctx,
			`ALTER ROLE `+role+` VALID UNTIL '`+source.ExpiresAt.Add(time.Minute).UTC().Format(time.RFC3339Nano)+`'`,
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := validateExtensionDatabaseKernelCoreState(ctx, expiryDrift); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
			t.Fatalf("kernel expiry drift validation error = %v", err)
		}
		if err := expiryDrift.Rollback(ctx); err != nil {
			t.Fatal(err)
		}

		membershipDrift, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer membershipDrift.Rollback(ctx)
		if err := lockExtensionDatabasePhysicalAuthority(ctx, membershipDrift); err != nil {
			t.Fatal(err)
		}
		if _, err := membershipDrift.Exec(ctx,
			`GRANT `+owner+` TO `+role+` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
		); err != nil {
			t.Fatal(err)
		}
		if _, _, err := validateExtensionDatabaseKernelCoreState(ctx, membershipDrift); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
			t.Fatalf("kernel SET membership drift validation error = %v", err)
		}
		if err := membershipDrift.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		targetRequest := ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: "membership-target",
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 958, AuditEventID: 959,
			},
		}
		target, err := registry.IssueRuntimeLease(ctx, targetRequest)
		if err != nil {
			t.Fatalf("issue target after membership restoration: %v", err)
		}
		if target.RoleName == source.RoleName {
			t.Fatal("restored membership target reused the source role")
		}
	})
}

func extensionDatabaseRuntimeCredentialRef(
	credential ExtensionDatabaseRuntimeCredential,
) ExtensionDatabaseRuntimeLeaseRef {
	return ExtensionDatabaseRuntimeLeaseRef{
		Artifact: credential.Artifact, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID,
	}
}

func execExtensionDatabaseKernelDDL(
	t *testing.T,
	ctx context.Context,
	connection *pgx.Conn,
	queries ...string,
) {
	t.Helper()
	tx, err := connection.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	for _, query := range queries {
		if _, err := tx.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertExtensionDatabaseKernelObjectOwner(
	t *testing.T,
	ctx context.Context,
	pool interface {
		QueryRow(context.Context, string, ...any) pgx.Row
	},
	kind string,
	schema string,
	name string,
	expected string,
) {
	t.Helper()
	var query string
	switch kind {
	case "relation":
		query = `
			SELECT owners.rolname
			FROM pg_class AS objects
			JOIN pg_namespace AS namespaces ON namespaces.oid = objects.relnamespace
			JOIN pg_roles AS owners ON owners.oid = objects.relowner
			WHERE namespaces.nspname = $1 AND objects.relname = $2
		`
	case "routine":
		query = `
			SELECT owners.rolname
			FROM pg_proc AS objects
			JOIN pg_namespace AS namespaces ON namespaces.oid = objects.pronamespace
			JOIN pg_roles AS owners ON owners.oid = objects.proowner
			WHERE namespaces.nspname = $1 AND objects.proname = $2
		`
	case "type":
		query = `
			SELECT owners.rolname
			FROM pg_type AS objects
			JOIN pg_namespace AS namespaces ON namespaces.oid = objects.typnamespace
			JOIN pg_roles AS owners ON owners.oid = objects.typowner
			WHERE namespaces.nspname = $1 AND objects.typname = $2
		`
	case "collation":
		query = `
			SELECT owners.rolname
			FROM pg_collation AS objects
			JOIN pg_namespace AS namespaces ON namespaces.oid = objects.collnamespace
			JOIN pg_roles AS owners ON owners.oid = objects.collowner
			WHERE namespaces.nspname = $1 AND objects.collname = $2
		`
	default:
		t.Fatalf("unknown kernel object kind %q", kind)
	}
	var actual string
	if err := pool.QueryRow(ctx, query, schema, name).Scan(&actual); err != nil {
		t.Fatalf("inspect %s %s.%s owner: %v", kind, schema, name, err)
	}
	if actual != expected {
		t.Fatalf("%s %s.%s owner = %s, want %s", kind, schema, name, actual, expected)
	}
}
