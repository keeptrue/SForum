package migrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func TestCoreMigratorReclaimsLiveKernelObjectsAndPreservesACL(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "live")
	lease := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.0.0", "live", coreKernelTestLeaseOptions{
		OwnSchema: true, SetActivePointer: true,
	})
	rawRoleName := "sforum_kraw_" + coreKernelTestDigest(fixture.databaseName)[:20]
	fixture.addCleanupRole(rawRoleName)
	if _, err := db.ExecContext(ctx,
		`CREATE ROLE `+pgx.Identifier{rawRoleName}.Sanitize()+` NOLOGIN`,
	); err != nil {
		t.Fatal(err)
	}
	connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
	tableName := "kernel_live_" + coreKernelTestDigest(lease.LeaseID)[:12]
	functionName := tableName + "_count"
	typeName := tableName + "_state"
	table := pgx.Identifier{coreauthority.PublicSchema, tableName}.Sanitize()
	function := pgx.Identifier{coreauthority.PublicSchema, functionName}.Sanitize()
	coreType := pgx.Identifier{coreauthority.PublicSchema, typeName}.Sanitize()
	for _, statement := range []string{
		`CREATE TYPE ` + coreType + ` AS ENUM ('ready')`,
		`CREATE TABLE ` + table + ` (id BIGINT PRIMARY KEY, state ` + coreType + ` NOT NULL)`,
		`CREATE FUNCTION ` + function + `() RETURNS BIGINT LANGUAGE SQL AS 'SELECT count(*) FROM ` + table + `'`,
		`GRANT SELECT, UPDATE ON TABLE ` + table + ` TO ` + pgx.Identifier{rawRoleName}.Sanitize(),
	} {
		if _, err := connection.Exec(ctx, statement); err != nil {
			connection.Close(context.Background())
			t.Fatalf("create live kernel Core object %q: %v", statement, err)
		}
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}

	if err := runCoreKernelTestUp(ctx, fixture, "1.1.0"); err != nil {
		t.Fatalf("restart migration with live kernel objects: %v", err)
	}
	assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+tableName, fixture.ownerRole)
	assertCoreAuthorityOwner(t, ctx, db, "routine", "public."+functionName, fixture.ownerRole)
	assertCoreAuthorityOwner(t, ctx, db, "type", "public."+typeName, fixture.ownerRole)
	var selectACL, updateACL, roleCanLogin bool
	if err := db.QueryRowContext(ctx, `
		SELECT has_table_privilege($1, $2, 'SELECT'),
		       has_table_privilege($1, $2, 'UPDATE'),
		       (SELECT rolcanlogin FROM pg_roles WHERE rolname = $3)
	`, rawRoleName, "public."+tableName, lease.RoleName).Scan(
		&selectACL, &updateACL, &roleCanLogin,
	); err != nil {
		t.Fatal(err)
	}
	if !selectACL || !updateACL || !roleCanLogin {
		t.Fatalf("post-migration live authority = select:%t update:%t login:%t", selectACL, updateACL, roleCanLogin)
	}
	if err := runCoreKernelTestUp(ctx, fixture, "1.1.0"); err != nil {
		t.Fatalf("repeat kernel ownership convergence: %v", err)
	}
}

func TestCoreMigratorReclaimsOverlappingAndPendingKernelObjectsTogether(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "overlap")
	source := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.0.0", "source", coreKernelTestLeaseOptions{
		OwnSchema: true, SetActivePointer: true,
	})
	target := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.1.0", "target", coreKernelTestLeaseOptions{
		OwnSchema: true, SetActivePointer: true,
	})
	pending := createCoreKernelTestLease(t, ctx, db, fixture, resource, "0.9.0", "pending", coreKernelTestLeaseOptions{
		OwnSchema: true,
	})
	leases := []coreKernelTestLease{source, target, pending}
	tableNames := make([]string, 0, len(leases))
	for index, lease := range leases {
		connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
		name := fmt.Sprintf("kernel_overlap_%d_%s", index, coreKernelTestDigest(lease.LeaseID)[:8])
		if _, err := connection.Exec(ctx,
			`CREATE TABLE `+pgx.Identifier{coreauthority.PublicSchema, name}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
		); err != nil {
			connection.Close(context.Background())
			t.Fatalf("create overlapping kernel object: %v", err)
		}
		if err := connection.Close(ctx); err != nil {
			t.Fatal(err)
		}
		tableNames = append(tableNames, name)
	}
	fenceCoreKernelTestLease(
		t, ctx, db, fixture, pending, coreauthority.KernelCleanupPendingExpiredCode,
	)

	if err := runCoreKernelTestUp(ctx, fixture, "1.2.0"); err != nil {
		t.Fatalf("restart migration with source, target, and pending kernel leases: %v", err)
	}
	for _, name := range tableNames {
		assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+name, fixture.ownerRole)
	}
	var live, fenced int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE rolcanlogin),
		       count(*) FILTER (WHERE NOT rolcanlogin AND rolvaliduntil = 'epoch'::timestamptz)
		FROM pg_roles WHERE rolname = ANY($1)
	`, []string{source.RoleName, target.RoleName, pending.RoleName}).Scan(&live, &fenced); err != nil {
		t.Fatal(err)
	}
	if live != 2 || fenced != 1 {
		t.Fatalf("overlap lease roles after convergence = live:%d fenced:%d", live, fenced)
	}
}

func TestCoreMigratorPreservesExactPendingUnsupportedOwnership(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "unsupported")
	lease := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.0.0", "unsupported", coreKernelTestLeaseOptions{
		SetActivePointer: true,
	})
	connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
	tableName := "kernel_supported_" + coreKernelTestDigest(lease.LeaseID)[:10]
	collationName := "kernel_collation_" + coreKernelTestDigest(lease.LeaseID)[:10]
	if _, err := connection.Exec(ctx,
		`CREATE TABLE `+pgx.Identifier{coreauthority.PublicSchema, tableName}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
	); err != nil {
		connection.Close(context.Background())
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx,
		`CREATE COLLATION `+pgx.Identifier{coreauthority.PublicSchema, collationName}.Sanitize()+` (provider = libc, locale = 'C')`,
	); err != nil {
		connection.Close(context.Background())
		t.Fatal(err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	fenceCoreKernelTestLease(
		t, ctx, db, fixture, lease, coreauthority.KernelCleanupPendingRevokeCode,
	)
	if err := runCoreKernelTestUp(ctx, fixture, "1.3.0"); err != nil {
		t.Fatalf("restart migration with exact pending collation: %v", err)
	}
	assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+tableName, fixture.ownerRole)
	assertCoreKernelCollationOwner(t, ctx, db, collationName, lease.RoleName)
	if err := runCoreKernelTestUp(ctx, fixture, "1.3.0"); err != nil {
		t.Fatalf("repeat migration with preserved pending collation: %v", err)
	}
}

func TestCoreMigratorRejectsForgedUnsupportedCoreOwner(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	identity := coreKernelTestDigest(fixture.databaseName + ":forged-collation")
	forgedRoleName := "sforum_kforged_" + identity[:20]
	collationName := "kernel_forged_" + identity[:10]
	fixture.addCleanupRole(forgedRoleName)
	forged := pgx.Identifier{forgedRoleName}.Sanitize()
	coreOwner := pgx.Identifier{fixture.ownerRole}.Sanitize()
	collation := pgx.Identifier{coreauthority.PublicSchema, collationName}.Sanitize()
	for _, statement := range []string{
		`CREATE ROLE ` + forged + ` NOLOGIN`,
		`GRANT ` + coreOwner + ` TO ` + forged + ` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		`CREATE COLLATION ` + collation + ` (provider = libc, locale = 'C')`,
		`ALTER COLLATION ` + collation + ` OWNER TO ` + forged,
		`REVOKE ` + coreOwner + ` FROM ` + forged,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare forged Core collation %q: %v", statement, err)
		}
	}
	err := runCoreKernelTestUp(ctx, fixture, "1.4.0")
	if !errors.Is(err, ErrCoreAuthorityConflict) || !strings.Contains(err.Error(), collationName) {
		t.Fatalf("forged unsupported Core owner error = %v", err)
	}
	assertCoreKernelCollationOwner(t, ctx, db, collationName, forgedRoleName)
}

func TestCoreUpgradeCompatibilityBlocksPhysicalKernelAuthority(t *testing.T) {
	cases := []struct {
		name    string
		pending bool
		rawCore bool
	}{
		{name: "active-kernel"},
		{name: "pending-kernel", pending: true},
		{name: "active-raw-core", rawCore: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newCoreAuthorityTestDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			db := prepareCoreKernelTestDatabase(t, fixture, ctx)
			resource := createCoreKernelTestResource(t, ctx, db, fixture, "compatibility-"+testCase.name)
			lease := createCoreKernelTestLease(
				t, ctx, db, fixture, resource, "1.0.0", "compatibility-"+testCase.name,
				coreKernelTestLeaseOptions{RawCore: testCase.rawCore},
			)
			pendingTableName := ""
			if testCase.pending {
				pendingTableName = "kernel_pending_compatibility_" + coreKernelTestDigest(lease.LeaseID)[:8]
				connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
				if _, err := connection.Exec(ctx,
					`CREATE TABLE `+pgx.Identifier{"public", pendingTableName}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
				); err != nil {
					connection.Close(context.Background())
					t.Fatal(err)
				}
				if err := connection.Close(ctx); err != nil {
					t.Fatal(err)
				}
				fenceCoreKernelTestLease(
					t, ctx, db, fixture, lease, coreauthority.KernelCleanupPendingExpiredCode,
				)
			}
			if _, err := db.ExecContext(ctx,
				`UPDATE extensions SET status = 'disabled', active_version_id = NULL WHERE id = $1`,
				resource.ExtensionID,
			); err != nil {
				t.Fatal(err)
			}
			if err := checkCoreUpgradeCompatibility(ctx, db, "1.9.9"); err != nil {
				t.Fatalf("compatible physical kernel lease was blocked: %v", err)
			}
			err := runCoreKernelTestUp(ctx, fixture, "2.0.0")
			if !errors.Is(err, ErrCoreUpgradeIncompatible) {
				t.Fatalf("incompatible physical kernel lease error = %v", err)
			}
			if pendingTableName != "" {
				assertCoreAuthorityOwner(
					t, ctx, db, "relation", "public."+pendingTableName, fixture.ownerRole,
				)
			}
		})
	}
}

func TestCoreUpgradeCompatibilityRejectsDriftedPhysicalRawCoreDeclaration(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "compatibility-raw-drift")
	lease := createCoreKernelTestLease(
		t, ctx, db, fixture, resource, "1.0.0", "compatibility-raw-drift",
		coreKernelTestLeaseOptions{RawCore: true},
	)
	if _, err := db.ExecContext(ctx, `
		UPDATE public.extensions
		SET status = 'disabled', active_version_id = NULL
		WHERE id = $1
	`, resource.ExtensionID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE public.extension_versions
		SET manifest = '{"database":{"grants":["core_views"],"coreCompatibility":">=1.0.0 <2.0.0"}}'::jsonb
		WHERE id = $1
	`, lease.VersionID); err != nil {
		t.Fatal(err)
	}
	var canUpdateCore bool
	if err := db.QueryRowContext(ctx,
		`SELECT has_table_privilege($1, 'public.users', 'UPDATE')`, lease.RoleName,
	).Scan(&canUpdateCore); err != nil {
		t.Fatal(err)
	}
	if !canUpdateCore {
		t.Fatal("drifted physical raw-core lease lost its Core DML authority before the gate")
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "1.9.0"); err != nil {
		t.Fatalf("lock-free declaration preflight inspected physical raw-core drift: %v", err)
	}
	if err := runCoreKernelTestUp(ctx, fixture, "1.9.0"); !errors.Is(err, ErrCoreAuthorityConflict) {
		t.Fatalf("drifted physical raw-core declaration error = %v", err)
	}
}

func TestCoreMigrationAuthorityReadsPublicKernelLedgerWithShadowSearchPath(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "shadow")
	lease := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.0.0", "shadow", coreKernelTestLeaseOptions{
		SetActivePointer: true,
	})
	connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
	tableName := "kernel_shadow_" + coreKernelTestDigest(lease.LeaseID)[:10]
	if _, err := connection.Exec(ctx,
		`CREATE TABLE `+pgx.Identifier{coreauthority.PublicSchema, tableName}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
	); err != nil {
		connection.Close(context.Background())
		t.Fatal(err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE SCHEMA kernel_shadow`,
		`CREATE TABLE kernel_shadow.extension_database_runtime_leases (LIKE public.extension_database_runtime_leases INCLUDING ALL)`,
		`CREATE TABLE kernel_shadow.extension_database_grant_powers (LIKE public.extension_database_grant_powers INCLUDING ALL)`,
		`CREATE TABLE kernel_shadow.extension_database_grants (LIKE public.extension_database_grants INCLUDING ALL)`,
		`CREATE TABLE kernel_shadow.extension_database_resources (LIKE public.extension_database_resources INCLUDING ALL)`,
		`CREATE TABLE kernel_shadow.extension_versions (LIKE public.extension_versions INCLUDING ALL)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare shadow kernel ledger %q: %v", statement, err)
		}
	}
	shadowConfig, err := pgx.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	shadowConfig.RuntimeParams["search_path"] = "kernel_shadow,public"
	shadowDB := stdlib.OpenDB(*shadowConfig)
	defer shadowDB.Close()
	if _, err := prepareCoreMigrationAuthorityForVersion(ctx, shadowDB, "1.7.0"); err != nil {
		t.Fatalf("converge public kernel ledger through shadow search_path: %v", err)
	}
	assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+tableName, fixture.ownerRole)
}

func TestCoreMigratorRejectsKernelAuthorityDrift(t *testing.T) {
	tests := []struct {
		name          string
		ownSchema     bool
		want          error
		rollbackOwner func(coreKernelTestLease) string
		mutate        func(*testing.T, context.Context, *sql.DB, *coreAuthorityTestDatabase, coreKernelTestLease)
	}{
		{
			name: "missing kernel power",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				_, err := db.ExecContext(ctx, `
					DELETE FROM public.extension_database_grant_powers
					WHERE grant_id = (
					  SELECT grant_id FROM public.extension_database_runtime_leases WHERE lease_id = $1
					) AND power = 'kernel'
				`, lease.LeaseID)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "manifest without kernel declaration",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx, `
					UPDATE public.extension_versions
					SET manifest = '{"database":{"grants":["core_views"],"coreCompatibility":">=1.0.0 <2.0.0"}}'::jsonb
					WHERE id = $1
				`, lease.VersionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "trusted compatibility constraint drift",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx, `
					UPDATE public.extension_versions
					SET manifest = jsonb_set(
						manifest, '{database,coreCompatibility}', to_jsonb('>=1.0.0 <3.0.0'::TEXT)
					)
					WHERE id = $1
				`, lease.VersionID); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `
					UPDATE public.extensions
					SET status = 'disabled', active_version_id = NULL
					WHERE id = $1
				`, lease.Resource.ExtensionID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra persisted grant power",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx, `
					INSERT INTO public.extension_database_grant_powers (grant_id, power, source, ordinal)
					SELECT grant_id, 'core_views', 'manifest_grants', 2
					FROM public.extension_database_runtime_leases WHERE lease_id = $1
				`, lease.LeaseID); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "missing exact extension version",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				statements := []struct {
					query string
					args  []any
				}{
					{`UPDATE public.extensions SET active_version_id = NULL WHERE id = $1`, []any{lease.Resource.ExtensionID}},
					{`DELETE FROM public.extension_versions WHERE id = $1`, []any{lease.VersionID}},
				}
				for _, statement := range statements {
					if _, err := db.ExecContext(ctx, statement.query, statement.args...); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "expiry drift",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				_, err := db.ExecContext(ctx,
					`ALTER ROLE `+pgx.Identifier{lease.RoleName}.Sanitize()+` VALID UNTIL '`+
						lease.LeaseExpiresAt.Add(time.Minute).Format(time.RFC3339Nano)+`'`,
				)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "lease role identity drift",
			rollbackOwner: func(lease coreKernelTestLease) string {
				return driftedCoreKernelLeaseRole(lease)
			},
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				driftedRole := driftedCoreKernelLeaseRole(lease)
				fixture.addCleanupRole(driftedRole)
				if _, err := db.ExecContext(ctx,
					`ALTER ROLE `+pgx.Identifier{lease.RoleName}.Sanitize()+` RENAME TO `+
						pgx.Identifier{driftedRole}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
				if _, err := db.ExecContext(ctx, `
					UPDATE public.extension_database_runtime_leases
					SET role_name = $2
					WHERE lease_id = $1
				`, lease.LeaseID, driftedRole); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "Core owner SET membership",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				_, err := db.ExecContext(ctx,
					`GRANT `+pgx.Identifier{fixture.ownerRole}.Sanitize()+` TO `+
						pgx.Identifier{lease.RoleName}.Sanitize()+` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
				)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "extra inherited role",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				extra := "sforum_kextra_" + coreKernelTestDigest(lease.LeaseID)[:20]
				fixture.addCleanupRole(extra)
				for _, statement := range []string{
					`CREATE ROLE ` + pgx.Identifier{extra}.Sanitize() + ` NOLOGIN`,
					`GRANT ` + pgx.Identifier{extra}.Sanitize() + ` TO ` + pgx.Identifier{lease.RoleName}.Sanitize(),
				} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "unknown failed code",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				fenceCoreKernelTestLease(t, ctx, db, fixture, lease, "lease_cleanup_pending.unknown")
			},
		},
		{
			name: "ledgerless Core owner member",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`DELETE FROM public.extension_database_runtime_leases WHERE lease_id = $1`, lease.LeaseID,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "direct Core ACL",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`GRANT SELECT ON TABLE public.users TO `+pgx.Identifier{lease.RoleName}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "self-granted Core ACL",
			mutate: func(t *testing.T, ctx context.Context, _ *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
				defer connection.Close(context.Background())
				if _, err := connection.Exec(ctx,
					`GRANT SELECT ON TABLE public.users TO `+pgx.Identifier{lease.RoleName}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "database CREATE",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`GRANT CREATE ON DATABASE `+pgx.Identifier{fixture.databaseName}.Sanitize()+` TO `+
						pgx.Identifier{lease.RoleName}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "extension owner membership drift",
			ownSchema: true,
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`REVOKE `+pgx.Identifier{lease.Resource.OwnerRole}.Sanitize()+` FROM `+
						pgx.Identifier{lease.RoleName}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "cross-extension resource identity drift",
			ownSchema: true,
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				foreign := createCoreKernelTestResource(t, ctx, db, fixture, "foreign-resource-"+lease.LeaseID)
				if _, err := db.ExecContext(ctx,
					`DELETE FROM public.extension_database_resources WHERE extension_id = $1`, foreign.ExtensionID,
				); err != nil {
					t.Fatal(err)
				}
				for _, statement := range []string{
					`REVOKE ` + pgx.Identifier{lease.Resource.OwnerRole}.Sanitize() + ` FROM ` + pgx.Identifier{lease.RoleName}.Sanitize(),
					`GRANT ` + pgx.Identifier{foreign.OwnerRole}.Sanitize() + ` TO ` + pgx.Identifier{lease.RoleName}.Sanitize() + ` WITH ADMIN FALSE, INHERIT TRUE, SET TRUE`,
				} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.ExecContext(ctx, `
					UPDATE public.extension_database_resources
					SET schema_name = $2, owner_role_name = $3, runtime_role_name = $4
					WHERE extension_id = $1
				`, lease.Resource.ExtensionID, foreign.Schema, foreign.OwnerRole, foreign.RuntimeRole); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name:      "unsafe extension owner attributes",
			ownSchema: true,
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`ALTER ROLE `+pgx.Identifier{lease.Resource.OwnerRole}.Sanitize()+` LOGIN`,
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "transitive lease member",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, fixture *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				member := "sforum_kmember_" + coreKernelTestDigest(lease.LeaseID)[:20]
				fixture.addCleanupRole(member)
				for _, statement := range []string{
					`CREATE ROLE ` + pgx.Identifier{member}.Sanitize() + ` NOLOGIN`,
					`GRANT ` + pgx.Identifier{lease.RoleName}.Sanitize() + ` TO ` + pgx.Identifier{member}.Sanitize(),
				} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "candidate-owned River object",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				if _, err := db.ExecContext(ctx,
					`ALTER TABLE public.river_job OWNER TO `+pgx.Identifier{lease.RoleName}.Sanitize(),
				); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "candidate-owned River routine",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				function := pgx.Identifier{"public", "river_kernel_drift"}.Sanitize()
				for _, statement := range []string{
					`CREATE FUNCTION ` + function + `() RETURNS BIGINT LANGUAGE SQL AS 'SELECT 1'`,
					`ALTER FUNCTION ` + function + `() OWNER TO ` + pgx.Identifier{lease.RoleName}.Sanitize(),
				} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
		{
			name: "candidate-owned River type",
			mutate: func(t *testing.T, ctx context.Context, db *sql.DB, _ *coreAuthorityTestDatabase, lease coreKernelTestLease) {
				coreType := pgx.Identifier{"public", "river_kernel_drift_state"}.Sanitize()
				for _, statement := range []string{
					`CREATE TYPE ` + coreType + ` AS ENUM ('ready')`,
					`ALTER TYPE ` + coreType + ` OWNER TO ` + pgx.Identifier{lease.RoleName}.Sanitize(),
				} {
					if _, err := db.ExecContext(ctx, statement); err != nil {
						t.Fatal(err)
					}
				}
			},
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			// 每个子用例自建数据库，物理权威锁按 current_database() 分键，
			// 彼此不共享状态；串行重放 152 条迁移是纯重复劳动。
			t.Parallel()
			fixture := newCoreAuthorityTestDatabase(t)
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			db := prepareCoreKernelTestDatabase(t, fixture, ctx)
			resource := createCoreKernelTestResource(t, ctx, db, fixture, "drift-"+testCase.name)
			lease := createCoreKernelTestLease(
				t, ctx, db, fixture, resource, "1.0.0", "drift-"+testCase.name,
				coreKernelTestLeaseOptions{OwnSchema: testCase.ownSchema, SetActivePointer: true},
			)
			rollbackTableName := "kernel_drift_rollback_" + coreKernelTestDigest(lease.LeaseID)[:10]
			connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
			if _, err := connection.Exec(ctx,
				`CREATE TABLE `+pgx.Identifier{"public", rollbackTableName}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
			); err != nil {
				connection.Close(context.Background())
				t.Fatal(err)
			}
			if err := connection.Close(ctx); err != nil {
				t.Fatal(err)
			}
			testCase.mutate(t, ctx, db, fixture, lease)
			err := runCoreKernelTestUp(ctx, fixture, "1.8.0")
			want := testCase.want
			if want == nil {
				want = ErrCoreAuthorityConflict
			}
			if !errors.Is(err, want) {
				t.Fatalf("kernel authority drift error = %v", err)
			}
			rollbackOwner := lease.RoleName
			if testCase.rollbackOwner != nil {
				rollbackOwner = testCase.rollbackOwner(lease)
			}
			assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+rollbackTableName, rollbackOwner)
		})
	}
}

func driftedCoreKernelLeaseRole(lease coreKernelTestLease) string {
	return "sforum_kdrift_" + coreKernelTestDigest(lease.LeaseID)[:20]
}

func TestCoreMigrationAuthorityPreservesKernelCreatorMembershipMultiset(t *testing.T) {
	fixture := newCoreAuthorityNonSuperTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	var serverVersion int
	if err := db.QueryRowContext(ctx, `SELECT current_setting('server_version_num')::INTEGER`).Scan(&serverVersion); err != nil {
		t.Fatal(err)
	}
	if serverVersion < 170000 {
		t.Skipf("PostgreSQL 17 grantor-specific memberships require PostgreSQL 17+, got %d", serverVersion)
	}
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "non-super")
	lease := createCoreKernelTestLease(t, ctx, db, fixture, resource, "1.0.0", "non-super", coreKernelTestLeaseOptions{
		OwnSchema: true, SetActivePointer: true,
	})
	connection := connectCoreKernelTestLease(t, ctx, fixture, lease)
	tableName := "kernel_non_super_" + coreKernelTestDigest(lease.LeaseID)[:10]
	if _, err := connection.Exec(ctx,
		`CREATE TABLE `+pgx.Identifier{coreauthority.PublicSchema, tableName}.Sanitize()+` (id BIGINT PRIMARY KEY)`,
	); err != nil {
		connection.Close(context.Background())
		t.Fatal(err)
	}
	if err := connection.Close(ctx); err != nil {
		t.Fatal(err)
	}
	before, err := loadCoreGrantedMemberships(ctx, mustBeginCoreKernelReadTx(t, ctx, db), lease.RoleName, fixture.sessionRole)
	if err != nil {
		t.Fatal(err)
	}
	if err := runCoreKernelTestUp(ctx, fixture, "1.5.0"); err != nil {
		t.Fatalf("non-super restart migration: %v", err)
	}
	after, err := loadCoreGrantedMemberships(ctx, mustBeginCoreKernelReadTx(t, ctx, db), lease.RoleName, fixture.sessionRole)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatalf("kernel creator membership changed: before=%#v after=%#v", before, after)
	}
	assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+tableName, fixture.ownerRole)
}

func TestCoreMigratorPublishesRuntimeVersionAndRejectsOlderTarget(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := runCoreKernelTestUp(ctx, fixture, "1.5.0"); err != nil {
		t.Fatalf("publish migrated Core runtime version: %v", err)
	}
	db := openCoreAuthorityFixtureDB(t, fixture)
	defer db.Close()
	var current, target, status string
	var revision int64
	if err := db.QueryRowContext(ctx, `
		SELECT current_version, target_version, status, revision
		FROM public.sforum_core_runtime_state
		WHERE singleton = TRUE
	`).Scan(&current, &target, &status, &revision); err != nil {
		t.Fatal(err)
	}
	if current != "1.5.0" || target != current || status != "ready" || revision < 3 {
		t.Fatalf("published Core runtime state = current:%q target:%q status:%q revision:%d", current, target, status, revision)
	}
	assertCoreAuthorityOwner(
		t, ctx, db, "relation", "public."+coreauthority.RuntimeStateTable, fixture.ownerRole,
	)
	if err := runCoreKernelTestUp(ctx, fixture, "1.5.0"); err != nil {
		t.Fatalf("repeat migrated Core runtime version: %v", err)
	}
	var repeatedCurrent, repeatedTarget, repeatedStatus string
	var repeatedRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT current_version, target_version, status, revision
		FROM public.sforum_core_runtime_state
		WHERE singleton = TRUE
	`).Scan(&repeatedCurrent, &repeatedTarget, &repeatedStatus, &repeatedRevision); err != nil {
		t.Fatal(err)
	}
	if repeatedCurrent != current || repeatedTarget != target || repeatedStatus != status || repeatedRevision != revision {
		t.Fatalf(
			"same-version Up changed published state: before=%q/%q/%q/%d after=%q/%q/%q/%d",
			current, target, status, revision,
			repeatedCurrent, repeatedTarget, repeatedStatus, repeatedRevision,
		)
	}
	if err := runCoreKernelTestUp(ctx, fixture, "1.4.0"); !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("older migrator target error = %v", err)
	}
	var afterCurrent, afterTarget, afterStatus string
	var afterRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT current_version, target_version, status, revision
		FROM public.sforum_core_runtime_state
		WHERE singleton = TRUE
	`).Scan(&afterCurrent, &afterTarget, &afterStatus, &afterRevision); err != nil {
		t.Fatal(err)
	}
	if afterCurrent != current || afterTarget != target || afterStatus != status || afterRevision != revision {
		t.Fatalf(
			"older target changed published state: before=%q/%q/%q/%d after=%q/%q/%q/%d",
			current, target, status, revision, afterCurrent, afterTarget, afterStatus, afterRevision,
		)
	}
	connection, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	_, marked, err := markCoreMigrationStarted(ctx, connection, "1.6.0", true)
	if err != nil || !marked {
		t.Fatalf("mark higher Core migration target: marked=%t err=%v", marked, err)
	}
	var failedCurrent, failedTarget, failedStatus string
	var failedRevision int64
	if err := db.QueryRowContext(ctx, `
		SELECT current_version, target_version, status, revision
		FROM public.sforum_core_runtime_state
		WHERE singleton = TRUE
	`).Scan(&failedCurrent, &failedTarget, &failedStatus, &failedRevision); err != nil {
		t.Fatal(err)
	}
	if failedCurrent != current || failedTarget != "1.6.0" || failedStatus != "migrating" ||
		failedRevision != revision+1 {
		t.Fatalf(
			"failed higher migration fence = current:%q target:%q status:%q revision:%d",
			failedCurrent, failedTarget, failedStatus, failedRevision,
		)
	}
}

func mustBeginCoreKernelReadTx(t *testing.T, ctx context.Context, db interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}) *sql.Tx {
	t.Helper()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback() })
	return tx
}
