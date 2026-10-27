package extensionsruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

func TestPostgresLifecycleMigrationEngineRunsConcurrentPlanOnce(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `
		CREATE TABLE migration_probe (id BIGINT PRIMARY KEY, note TEXT NOT NULL);
		INSERT INTO migration_probe (id, note) VALUES (1, 'once');
	`, "required")

	const workers = 8
	errorsByWorker := make([]error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			errorsByWorker[index] = fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan)
		}(index)
	}
	wait.Wait()
	for index, err := range errorsByWorker {
		if err != nil {
			t.Fatalf("worker %d failed: %v", index, err)
		}
	}

	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if !proof.TargetReady || proof.SourceResumeSafe || proof.PlanDigest != fixture.plan.PlanDigest {
		t.Fatalf("unexpected concurrent proof: %#v", proof)
	}
	var note string
	query := `SELECT note FROM ` + pgx.Identifier{fixture.identifiers.Schema, "migration_probe"}.Sanitize() + ` WHERE id = 1`
	if err := fixture.pool.QueryRow(fixture.ctx, query).Scan(&note); err != nil {
		t.Fatal(err)
	}
	if note != "once" {
		t.Fatalf("unexpected migration result %q", note)
	}
	var plans, appliedSteps, stateRows int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_migration_plans WHERE extension_id = $1),
		  (SELECT count(*) FROM extension_database_migration_steps AS steps
		   JOIN extension_database_migration_plans AS plans ON plans.id = steps.plan_id
		   WHERE plans.extension_id = $1 AND steps.status = 'applied'),
		  (SELECT count(*) FROM extension_database_migration_state WHERE extension_id = $1)
	`, fixture.extensionID).Scan(&plans, &appliedSteps, &stateRows); err != nil {
		t.Fatal(err)
	}
	if plans != 1 || appliedSteps != 1 || stateRows != 1 {
		t.Fatalf("migration was not once-only: plans=%d applied=%d state=%d", plans, appliedSteps, stateRows)
	}
	migrationRole, err := ExtensionDatabaseMigrationRoleFor(fixture.extensionID, fixture.plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	var canLogin, remainsOwnerMember, settingsCleared bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT roles.rolcanlogin,
		       pg_has_role($1, $2, 'MEMBER'),
		       NOT EXISTS (
		         SELECT 1 FROM pg_db_role_setting
		         WHERE setrole = roles.oid
		       )
		FROM pg_roles AS roles WHERE roles.rolname = $1
	`, migrationRole, fixture.identifiers.OwnerRole).Scan(
		&canLogin, &remainsOwnerMember, &settingsCleared,
	); err != nil {
		t.Fatal(err)
	}
	if canLogin || remainsOwnerMember || !settingsCleared {
		t.Fatalf("retired migration role is not inert: login=%v owner_member=%v settings_cleared=%v",
			canLogin, remainsOwnerMember, settingsCleared)
	}
}

func TestPostgresLifecycleMigrationEngineTransactionalFailureRollsBackAndRetriesSafely(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `
		CREATE TABLE rollback_probe (id BIGINT PRIMARY KEY);
		INSERT INTO table_that_does_not_exist (id) VALUES (1);
	`, "required")
	if err := fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan); err == nil {
		t.Fatal("failing transactional migration succeeded")
	}
	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if proof.TargetReady || !proof.SourceResumeSafe {
		t.Fatalf("transaction rollback proof is unsafe: %#v", proof)
	}
	if extensionDatabaseRelationExists(t, fixture, "rollback_probe") {
		t.Fatal("transactional failure left its table behind")
	}

	retry := fixture.plan
	retry.Attempt = 2
	if err := fixture.engine.ReconcileLifecycleMigration(fixture.ctx, retry); err == nil {
		t.Fatal("retry unexpectedly succeeded")
	}
	retryProof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, retry)
	if err != nil {
		t.Fatal(err)
	}
	if retryProof.TargetReady || !retryProof.SourceResumeSafe {
		t.Fatalf("retry lost rollback safety: %#v", retryProof)
	}
	var attempt int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT attempt FROM extension_database_migration_plans WHERE plan_digest = $1
	`, fixture.plan.PlanDigest).Scan(&attempt); err != nil {
		t.Fatal(err)
	}
	if attempt != 2 {
		t.Fatalf("retry attempt=%d want 2", attempt)
	}
}

func TestPostgresLifecycleMigrationEngineRejectsExactFileChecksumDrift(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `CREATE TABLE checksum_probe (id BIGINT);`, "required")
	if err := os.WriteFile(fixture.migrationPath, []byte(`CREATE TABLE tampered_probe (id BIGINT);`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan); err == nil {
		t.Fatal("tampered exact migration succeeded")
	}
	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if proof.TargetReady || !proof.SourceResumeSafe {
		t.Fatalf("checksum drift proof is wrong: %#v", proof)
	}
	var failureCode string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT failure_code FROM extension_database_migration_plans WHERE plan_digest = $1
	`, fixture.plan.PlanDigest).Scan(&failureCode); err != nil {
		t.Fatal(err)
	}
	if failureCode != extensionDatabaseMigrationFailureChecksumDrift {
		t.Fatalf("failure_code=%q", failureCode)
	}
	if extensionDatabaseRelationExists(t, fixture, "tampered_probe") {
		t.Fatal("tampered migration executed")
	}
}

func TestPostgresLifecycleMigrationEngineRecoversReleasedAdvisoryLock(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `CREATE TABLE lock_probe (id BIGINT);`, "required")
	connection, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(fixture.ctx, `SELECT pg_advisory_lock($1)`, fixture.identifiers.LockKey); err != nil {
		connection.Release()
		t.Fatal(err)
	}
	if err := connection.Conn().Close(fixture.ctx); err != nil {
		connection.Release()
		t.Fatal(err)
	}
	connection.Release()

	if err := fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan); err != nil {
		t.Fatalf("migration did not recover released advisory lock: %v", err)
	}
	if !extensionDatabaseRelationExists(t, fixture, "lock_probe") {
		t.Fatal("migration did not execute after lock recovery")
	}
}

func TestPostgresLifecycleMigrationEngineRecordsNonTransactionalUnsafeFailure(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `
		CREATE TABLE non_transactional_probe (id BIGINT);
		INSERT INTO non_transactional_missing (id) VALUES (1);
	`, "forbidden")
	if err := fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan); err == nil {
		t.Fatal("failing non-transactional migration succeeded")
	}
	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if proof.TargetReady || proof.SourceResumeSafe {
		t.Fatalf("non-transactional proof incorrectly permits source resume: %#v", proof)
	}
	if !extensionDatabaseRelationExists(t, fixture, "non_transactional_probe") {
		t.Fatal("test did not cross a non-transactional side-effect boundary")
	}
	var warningCode, failureCode string
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT warning_code, failure_code
		FROM extension_database_migration_plans WHERE plan_digest = $1
	`, fixture.plan.PlanDigest).Scan(&warningCode, &failureCode); err != nil {
		t.Fatal(err)
	}
	if warningCode != extensionDatabaseMigrationWarningNonTransactional ||
		failureCode != extensionDatabaseMigrationFailureExecution {
		t.Fatalf("warning=%q failure=%q", warningCode, failureCode)
	}
}

func TestPostgresLifecycleMigrationEngineRejectsPreexistingMigrationRoleAuthority(t *testing.T) {
	fixture := newExtensionDatabaseMigrationEngineFixture(t, `CREATE TABLE malicious_role_probe (id BIGINT);`, "required")
	migrationRole, err := ExtensionDatabaseMigrationRoleFor(fixture.extensionID, fixture.plan.PlanDigest)
	if err != nil {
		t.Fatal(err)
	}
	highDigest := sha256.Sum256([]byte(fixture.extensionID + ":migration-high"))
	highRole := "sforum_ext_test_mhigh_" + hex.EncodeToString(highDigest[:8])
	if _, err := fixture.pool.Exec(fixture.ctx, `CREATE ROLE `+pgx.Identifier{highRole}.Sanitize()+` NOLOGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `CREATE ROLE `+pgx.Identifier{migrationRole}.Sanitize()+` NOLOGIN NOINHERIT`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `GRANT SELECT ON public.extensions TO `+pgx.Identifier{highRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `GRANT `+pgx.Identifier{highRole}.Sanitize()+` TO `+pgx.Identifier{migrationRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = fixture.pool.Exec(context.Background(), `REVOKE `+pgx.Identifier{highRole}.Sanitize()+` FROM `+pgx.Identifier{migrationRole}.Sanitize())
		_, _ = fixture.pool.Exec(context.Background(), `DROP OWNED BY `+pgx.Identifier{highRole}.Sanitize())
		_, _ = fixture.pool.Exec(context.Background(), `DROP ROLE IF EXISTS `+pgx.Identifier{highRole}.Sanitize())
	}()

	err = fixture.engine.ReconcileLifecycleMigration(fixture.ctx, fixture.plan)
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("preexisting migration role authority was not rejected: %v", err)
	}
	proof, err := fixture.engine.InspectLifecycleMigration(fixture.ctx, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	if proof.TargetReady || !proof.SourceResumeSafe {
		t.Fatalf("malicious role failure proof is unsafe: %#v", proof)
	}
	var remainsMember, canLogin bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT pg_has_role($1, $2, 'MEMBER'), rolcanlogin
		FROM pg_roles WHERE rolname = $1
	`, migrationRole, highRole).Scan(&remainsMember, &canLogin); err != nil {
		t.Fatal(err)
	}
	if !remainsMember || canLogin || extensionDatabaseRelationExists(t, fixture, "malicious_role_probe") {
		t.Fatalf("failed engine mutated/executed malicious role: member=%v login=%v", remainsMember, canLogin)
	}
}

func TestReleaseExtensionDatabaseMigrationLockDiscardsUnprovenSession(t *testing.T) {
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var firstPID int32
	if err := connection.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&firstPID); err != nil {
		connection.Release()
		t.Fatal(err)
	}
	// This session never acquired the key, so pg_advisory_unlock returns false.
	// The release helper must discard it rather than returning it to MaxConns=1.
	releaseExtensionDatabaseSessionLock(connection, extensionDatabaseAdvisoryKey("p5.unlock.failure"))

	replacement, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Release()
	var replacementPID int32
	if err := replacement.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&replacementPID); err != nil {
		t.Fatal(err)
	}
	if replacementPID == firstPID {
		t.Fatalf("unproven lock session %d returned to pool", firstPID)
	}
	lockKey := extensionDatabaseAdvisoryKey("p5.unlock.recovery")
	var locked, unlocked bool
	if err := replacement.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, lockKey).Scan(&locked); err != nil || !locked {
		t.Fatalf("replacement session could not lock: locked=%v err=%v", locked, err)
	}
	if err := replacement.QueryRow(ctx, `SELECT pg_advisory_unlock($1)`, lockKey).Scan(&unlocked); err != nil || !unlocked {
		t.Fatalf("replacement session could not unlock: unlocked=%v err=%v", unlocked, err)
	}
}

type extensionDatabaseMigrationEngineFixture struct {
	ctx           context.Context
	pool          *pgxpool.Pool
	engine        *PostgresLifecycleMigrationEngine
	plan          LifecycleMigrationEnginePlan
	extensionID   string
	identifiers   ExtensionDatabaseIdentifiers
	migrationPath string
}

func newExtensionDatabaseMigrationEngineFixture(
	t *testing.T,
	body string,
	transactionPolicy string,
) extensionDatabaseMigrationEngineFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	extensionID := fmt.Sprintf("p5.engine.%d", time.Now().UnixNano())
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	packageRoot := t.TempDir()
	migrationRelativePath := "migrations/001.sql"
	migrationPath := filepath.Join(packageRoot, filepath.FromSlash(migrationRelativePath))
	if err := os.MkdirAll(filepath.Dir(migrationPath), 0o700); err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	bodyBytes := []byte(strings.TrimSpace(body) + "\n")
	if err := os.WriteFile(migrationPath, bodyBytes, 0o600); err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	migrationDigestBytes := sha256.Sum256(bodyBytes)
	migrationDigest := hex.EncodeToString(migrationDigestBytes[:])
	migrationID := extensionID + ".migration.initial"
	migration := extensions.ManifestMigration{
		ID: migrationID, ContractVersion: migrationID + "@1",
		Path: migrationRelativePath, Digest: migrationDigest, Transaction: transactionPolicy,
	}
	manifest := extensions.Manifest{
		ID: extensionID, Name: "P5 migration fixture", Version: "1.0.0", Type: extensions.TypePlugin,
		Migrations: []extensions.ManifestMigration{migration},
		Database: &extensions.ManifestDatabase{
			ContractVersion: extensionID + ".database@1", Authority: "own_schema",
		},
		PackageFiles: []extensions.ManifestPackageFile{{
			ID: extensionID + ".file.migration", Kind: "migration",
			Path: migrationRelativePath, Digest: migrationDigest,
		}},
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	packageDigestBytes := sha256.Sum256([]byte(extensionID + ":package"))
	packageDigest := hex.EncodeToString(packageDigestBytes[:])
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'P5 migration fixture', 'installed')
	`, extensionID); err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	var versionID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, '1.0.0', $2::jsonb, $3, $4)
		RETURNING id
	`, extensionID, manifestJSON, packageRoot, packageDigest).Scan(&versionID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	operationKey := fmt.Sprintf("p5-engine-%d", time.Now().UnixNano())
	requestFingerprintBytes := sha256.Sum256([]byte(operationKey))
	var operationID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, operation,
			plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot
		) VALUES ($1, '1.0.0', $2, 'install', 'p5.database@1', $3, $4, 'builtin', '{}'::jsonb)
		RETURNING id
	`, extensionID, packageDigest, operationKey,
		hex.EncodeToString(requestFingerprintBytes[:])).Scan(&operationID); err != nil {
		_, _ = pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, extensionID)
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	declarations, migrationsDigest, err := lifecycleMigrationDeclarations(manifest.Migrations)
	if err != nil {
		pool.Close()
		cancel()
		t.Fatal(err)
	}
	planDigestBytes := sha256.Sum256([]byte(extensionID + ":plan"))
	plan := LifecycleMigrationEnginePlan{
		OperationID: operationID, Operation: extensions.LifecycleMachineInstall,
		StepID: "lifecycle.install.02.host.migrating", Attempt: 1,
		Mode: LifecycleBoundaryMigrationInstall, PlanDigest: hex.EncodeToString(planDigestBytes[:]),
		Target: LifecycleMigrationArtifact{
			ExtensionID: extensionID, Version: "1.0.0", PackageDigest: packageDigest,
			VersionID: versionID, MigrationsDigest: migrationsDigest, Migrations: declarations,
		},
	}
	fixture := extensionDatabaseMigrationEngineFixture{
		ctx: ctx, pool: pool, engine: NewPostgresLifecycleMigrationEngine(pool, nil),
		plan: plan, extensionID: extensionID, identifiers: identifiers, migrationPath: migrationPath,
	}
	t.Cleanup(func() {
		cleanupExtensionDatabaseMigrationEngineFixture(t, fixture)
		pool.Close()
		cancel()
	})
	return fixture
}

func extensionDatabaseRelationExists(
	t *testing.T,
	fixture extensionDatabaseMigrationEngineFixture,
	name string,
) bool {
	t.Helper()
	var exists bool
	qualified := fixture.identifiers.Schema + "." + name
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT to_regclass($1) IS NOT NULL`, qualified).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	return exists
}

func cleanupExtensionDatabaseMigrationEngineFixture(
	t *testing.T,
	fixture extensionDatabaseMigrationEngineFixture,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	migrationRole, _ := ExtensionDatabaseMigrationRoleFor(fixture.extensionID, fixture.plan.PlanDigest)
	for _, role := range []string{fixture.identifiers.RuntimeRole, migrationRole} {
		_, _ = fixture.pool.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE usename = $1`, role)
	}
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_migration_proofs WHERE plan_id IN (SELECT id FROM extension_database_migration_plans WHERE extension_id = $1)`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_migration_state WHERE extension_id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_migration_steps WHERE plan_id IN (SELECT id FROM extension_database_migration_plans WHERE extension_id = $1)`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_migration_plans WHERE extension_id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_credentials WHERE extension_id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_grants WHERE extension_id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_database_resources WHERE extension_id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extension_lifecycle_operations WHERE id = $1`, fixture.plan.OperationID)
	_, _ = fixture.pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, fixture.extensionID)
	_, _ = fixture.pool.Exec(ctx, `DROP SCHEMA IF EXISTS `+pgx.Identifier{fixture.identifiers.Schema}.Sanitize()+` CASCADE`)
	for _, role := range []string{migrationRole, fixture.identifiers.RuntimeRole, fixture.identifiers.OwnerRole} {
		if role == "" {
			continue
		}
		quoted := pgx.Identifier{role}.Sanitize()
		_, _ = fixture.pool.Exec(ctx, `DROP OWNED BY `+quoted)
		_, _ = fixture.pool.Exec(ctx, `DROP ROLE IF EXISTS `+quoted)
	}
}
