package migrator

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

type coreKernelTestResource struct {
	ExtensionID string
	OwnerRole   string
	Schema      string
	RuntimeRole string
}

type coreKernelTestLease struct {
	Resource       coreKernelTestResource
	RoleName       string
	Version        string
	VersionID      int64
	PackageDigest  string
	LeaseID        string
	Password       string
	LeaseExpiresAt time.Time
}

type coreKernelTestLeaseOptions struct {
	OwnSchema        bool
	RawCore          bool
	SetActivePointer bool
	Compatibility    string
}

func prepareCoreKernelTestDatabase(
	t *testing.T,
	fixture *coreAuthorityTestDatabase,
	ctx context.Context,
) *sql.DB {
	t.Helper()
	if err := runCoreKernelTestUp(ctx, fixture, "1.0.0"); err != nil {
		t.Fatalf("prepare isolated kernel migration database: %v", err)
	}
	db := openCoreAuthorityFixtureDB(t, fixture)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func runCoreKernelTestUp(
	ctx context.Context,
	fixture *coreAuthorityTestDatabase,
	targetVersion string,
) error {
	return Up(ctx, Config{
		DatabaseURL:       fixture.databaseURL,
		TargetCoreVersion: targetVersion,
		Logger: slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{
			Level: slog.LevelError,
		})),
	})
}

func createCoreKernelTestResource(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	fixture *coreAuthorityTestDatabase,
	suffix string,
) coreKernelTestResource {
	t.Helper()
	identity := coreKernelTestDigest(fixture.databaseName + ":resource:" + suffix)
	extensionID := "p5.kernel.migrator." + identity[:16]
	identifiers, err := coreauthority.ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	resource := coreKernelTestResource{
		ExtensionID: extensionID,
		OwnerRole:   identifiers.OwnerRole,
		Schema:      identifiers.Schema,
		RuntimeRole: identifiers.RuntimeRole,
	}
	fixture.addCleanupRole(resource.OwnerRole)
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extensions (id, type, name, status, source, is_system)
		VALUES ($1, 'plugin', 'Kernel migrator fixture', 'enabled', 'uploaded', false)
	`, resource.ExtensionID); err != nil {
		t.Fatalf("insert kernel migrator extension: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_resources (
			extension_id, schema_name, owner_role_name, runtime_role_name
		) VALUES ($1, $2, $3, $4)
	`, resource.ExtensionID, resource.Schema, resource.OwnerRole,
		resource.RuntimeRole); err != nil {
		t.Fatalf("insert kernel migrator database resource: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockCorePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	owner := pgx.Identifier{resource.OwnerRole}.Sanitize()
	session := pgx.Identifier{fixture.sessionRole}.Sanitize()
	schema := pgx.Identifier{resource.Schema}.Sanitize()
	for _, statement := range []string{
		`CREATE ROLE ` + owner + ` NOLOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`GRANT ` + owner + ` TO ` + session + ` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
		`CREATE SCHEMA ` + schema + ` AUTHORIZATION ` + owner,
		`REVOKE ` + owner + ` FROM ` + session + ` GRANTED BY CURRENT_USER`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare kernel migrator resource %q: %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return resource
}

func createCoreKernelTestLease(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	fixture *coreAuthorityTestDatabase,
	resource coreKernelTestResource,
	version string,
	suffix string,
	options coreKernelTestLeaseOptions,
) coreKernelTestLease {
	t.Helper()
	compatibility := options.Compatibility
	if compatibility == "" {
		// 这条声明是被 checkCoreUpgradeCompatibility 拿「本组用例自己选的
		// 迁移目标」校验的（runCoreKernelTestUp 传 1.0.0 … 1.9.0），不是拿
		// 当前宿主校验，所以**不能**像 extensionsruntime 的高风险夹具那样从
		// CoreCompatibilityVersion() 派生：dev 宿主是 3.0.16+…，派生会让所有
		// 1.x 目标当场被判不兼容。这里考的是 kernel 资源回收与权限漂移。
		compatibility = ">=1.0.0 <2.0.0"
	}
	lease := coreKernelTestLease{
		Resource:       resource,
		Version:        version,
		PackageDigest:  coreKernelTestDigest(resource.ExtensionID + ":package:" + suffix),
		LeaseID:        coreKernelTestDigest(resource.ExtensionID + ":lease-id:" + suffix),
		Password:       "sforum_kernel_migrator_test",
		LeaseExpiresAt: time.Now().UTC().Add(10 * time.Minute).Truncate(time.Microsecond),
	}
	roleName, err := coreauthority.ExtensionDatabaseRuntimeLeaseRoleFor(
		resource.ExtensionID, suffix, lease.LeaseID,
	)
	if err != nil {
		t.Fatal(err)
	}
	lease.RoleName = roleName
	fixture.addCleanupRole(lease.RoleName)
	manifest := fmt.Sprintf(
		`{"database":{"grants":["kernel"],"coreCompatibility":%q}}`,
		compatibility,
	)
	if options.RawCore {
		manifest = fmt.Sprintf(
			`{"database":{"grants":["raw_core"],"coreCompatibility":%q}}`,
			compatibility,
		)
	}
	if options.OwnSchema && !options.RawCore {
		manifest = fmt.Sprintf(
			`{"database":{"grants":["own_schema","kernel"],"coreCompatibility":%q}}`,
			compatibility,
		)
	} else if options.OwnSchema {
		manifest = fmt.Sprintf(
			`{"database":{"grants":["own_schema","raw_core"],"coreCompatibility":%q}}`,
			compatibility,
		)
	}
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_versions (
			extension_id, version, manifest, package_path, package_digest
		) VALUES ($1, $2, $3::jsonb, '/tmp/kernel-migrator-fixture', $4)
		RETURNING id
	`, resource.ExtensionID, version, manifest, lease.PackageDigest).Scan(&lease.VersionID); err != nil {
		t.Fatalf("insert kernel migrator extension version: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action,
			artifact_digests, impact_document, impact_digest
		) VALUES ($1, $2, $3, 'enable', '{}'::jsonb, $4::jsonb, $5)
	`, resource.ExtensionID, version, lease.PackageDigest, manifest,
		coreKernelTestDigest(resource.ExtensionID+":trust:"+suffix)); err != nil {
		t.Fatalf("insert exact kernel migrator trust: %v", err)
	}
	if options.SetActivePointer {
		if _, err := db.ExecContext(ctx,
			`UPDATE extensions SET status = 'enabled', active_version_id = $2 WHERE id = $1`,
			resource.ExtensionID, lease.VersionID,
		); err != nil {
			t.Fatalf("select active kernel migrator version: %v", err)
		}
	}
	var grantID int64
	if err := db.QueryRowContext(ctx, `
		INSERT INTO extension_database_grants (
			extension_id, extension_version_id, extension_version, package_digest,
			database_contract_version, authority, granted_by_user_id, grant_audit_event_id
		) VALUES ($1, $2, $3, $4, 'sforum.database@1', 'additive', 1, 1)
		RETURNING id
	`, resource.ExtensionID, lease.VersionID, version, lease.PackageDigest).Scan(&grantID); err != nil {
		t.Fatalf("insert kernel migrator grant: %v", err)
	}
	if options.OwnSchema {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO extension_database_grant_powers (grant_id, power, source, ordinal)
			VALUES ($1, 'own_schema', 'manifest_grants', 1)
		`, grantID); err != nil {
			t.Fatalf("insert own-schema kernel migrator power: %v", err)
		}
	}
	highRiskPower := "kernel"
	highRiskOrdinal := 5
	if options.RawCore {
		highRiskPower = "raw_core"
		highRiskOrdinal = 4
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_grant_powers (grant_id, power, source, ordinal)
		VALUES ($1, $2, 'manifest_grants', $3)
	`, grantID, highRiskPower, highRiskOrdinal); err != nil {
		t.Fatalf("insert high-risk kernel migrator power: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO extension_database_runtime_leases (
			lease_id, grant_id, extension_id, extension_version_id,
			extension_version, package_digest, runtime_instance_id, role_name,
			credential_fingerprint, issued_by, issue_audit_event_id,
			lease_expires_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'host', 1, $10)
	`, lease.LeaseID, grantID, resource.ExtensionID, lease.VersionID,
		version, lease.PackageDigest, suffix, lease.RoleName,
		coreKernelTestDigest(lease.Password), lease.LeaseExpiresAt); err != nil {
		t.Fatalf("insert kernel migrator runtime lease: %v", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockCorePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{lease.RoleName}.Sanitize()
	coreOwner := pgx.Identifier{fixture.ownerRole}.Sanitize()
	database := pgx.Identifier{fixture.databaseName}.Sanitize()
	searchPath := pgx.Identifier{coreauthority.PublicSchema}.Sanitize() + `, pg_catalog`
	statements := []string{
		`CREATE ROLE ` + role + ` LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 1 VALID UNTIL '` + lease.LeaseExpiresAt.Format(time.RFC3339Nano) + `' PASSWORD '` + lease.Password + `'`,
		`GRANT CONNECT ON DATABASE ` + database + ` TO ` + role,
	}
	if options.RawCore {
		statements = append(statements,
			`GRANT USAGE ON SCHEMA public TO `+role,
			`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE public.users TO `+role,
		)
	} else {
		statements = append(statements,
			`GRANT `+coreOwner+` TO `+role+` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		)
	}
	if options.OwnSchema {
		owner := pgx.Identifier{resource.OwnerRole}.Sanitize()
		statements = append(statements,
			`GRANT `+owner+` TO `+role+` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
		)
		searchPath = pgx.Identifier{resource.Schema}.Sanitize() + `, ` + searchPath
	}
	statements = append(statements,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET search_path TO `+searchPath,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET statement_timeout TO '5s'`,
		`ALTER ROLE `+role+` IN DATABASE `+database+` SET idle_in_transaction_session_timeout TO '15s'`,
	)
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare kernel migrator lease %q: %v", statement, err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return lease
}

func connectCoreKernelTestLease(
	t *testing.T,
	ctx context.Context,
	fixture *coreAuthorityTestDatabase,
	lease coreKernelTestLease,
) *pgx.Conn {
	t.Helper()
	config, err := pgx.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	delete(config.RuntimeParams, "role")
	config.User = lease.RoleName
	config.Password = lease.Password
	connection, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatalf("connect exact kernel lease %s: %v", lease.RoleName, err)
	}
	return connection
}

func fenceCoreKernelTestLease(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	fixture *coreAuthorityTestDatabase,
	lease coreKernelTestLease,
	failureCode string,
) {
	t.Helper()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockCorePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{lease.RoleName}.Sanitize()
	database := pgx.Identifier{fixture.databaseName}.Sanitize()
	for _, statement := range []string{
		`ALTER ROLE ` + role + ` NOLOGIN PASSWORD NULL VALID UNTIL 'epoch'`,
		`REVOKE CONNECT ON DATABASE ` + database + ` FROM ` + role,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("fence kernel migrator lease: %v", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE extension_database_runtime_leases
		SET status = 'failed', failure_code = $2, revoked_by = 'host',
		    revoke_audit_event_id = 1, revoked_at = statement_timestamp(),
		    lease_revision = lease_revision + 1
		WHERE lease_id = $1
	`, lease.LeaseID, failureCode); err != nil {
		t.Fatalf("persist fenced kernel migrator lease: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func coreKernelTestDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func assertCoreKernelCollationOwner(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	name string,
	want string,
) {
	t.Helper()
	var owner string
	if err := db.QueryRowContext(ctx, `
		SELECT owners.rolname
		FROM pg_collation AS collations
		JOIN pg_namespace AS namespaces ON namespaces.oid = collations.collnamespace
		JOIN pg_roles AS owners ON owners.oid = collations.collowner
		WHERE namespaces.nspname = $1 AND collations.collname = $2
	`, coreauthority.PublicSchema, name).Scan(&owner); err != nil {
		t.Fatalf("inspect Core collation %s owner: %v", name, err)
	}
	if owner != want {
		t.Fatalf("Core collation %s owner = %s, want %s", name, owner, want)
	}
}
