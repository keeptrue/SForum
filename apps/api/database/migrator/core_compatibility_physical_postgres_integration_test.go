package migrator

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestCoreUpgradeCompatibilityRechecksPhysicalAuthorityUnderLock(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	db := prepareCoreKernelTestDatabase(t, fixture, ctx)
	resource := createCoreKernelTestResource(t, ctx, db, fixture, "compatibility-lock")
	lease := createCoreKernelTestLease(
		t, ctx, db, fixture, resource, "1.0.0", "compatibility-lock",
		coreKernelTestLeaseOptions{SetActivePointer: true},
	)
	role := pgx.Identifier{lease.RoleName}.Sanitize()
	coreOwner := pgx.Identifier{fixture.ownerRole}.Sanitize()
	database := pgx.Identifier{fixture.databaseName}.Sanitize()

	dropTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer dropTx.Rollback()
	if err := lockCorePhysicalAuthority(ctx, dropTx); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`REVOKE ` + coreOwner + ` FROM ` + role + ` GRANTED BY CURRENT_USER`,
		`DROP OWNED BY ` + role,
		`DROP ROLE ` + role,
	} {
		if _, err := dropTx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("remove physical lease before compatibility check %q: %v", statement, err)
		}
	}
	if err := dropTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE public.extensions
		SET status = 'disabled', active_version_id = NULL
		WHERE id = $1
	`, resource.ExtensionID); err != nil {
		t.Fatal(err)
	}
	if err := checkCoreUpgradeCompatibility(ctx, db, "2.0.0", true); err != nil {
		t.Fatalf("ledger without a physical role unexpectedly blocked compatibility: %v", err)
	}

	issueTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer issueTx.Rollback()
	if err := lockCorePhysicalAuthority(ctx, issueTx); err != nil {
		t.Fatal(err)
	}
	tableName := "kernel_compatibility_lock_" + coreKernelTestDigest(lease.LeaseID)[:10]
	table := pgx.Identifier{"public", tableName}.Sanitize()
	for _, statement := range []string{
		`CREATE ROLE ` + role + ` LOGIN INHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS CONNECTION LIMIT 1 VALID UNTIL '` + lease.LeaseExpiresAt.Format(time.RFC3339Nano) + `' PASSWORD '` + lease.Password + `'`,
		`GRANT ` + coreOwner + ` TO ` + role + ` WITH ADMIN FALSE, INHERIT TRUE, SET FALSE`,
		`GRANT CONNECT ON DATABASE ` + database + ` TO ` + role,
		`ALTER ROLE ` + role + ` IN DATABASE ` + database + ` SET search_path TO public, pg_catalog`,
		`ALTER ROLE ` + role + ` IN DATABASE ` + database + ` SET statement_timeout TO '5s'`,
		`ALTER ROLE ` + role + ` IN DATABASE ` + database + ` SET idle_in_transaction_session_timeout TO '15s'`,
		`CREATE TABLE ` + table + ` (id BIGINT PRIMARY KEY)`,
		`ALTER TABLE ` + table + ` OWNER TO ` + role,
	} {
		if _, err := issueTx.ExecContext(ctx, statement); err != nil {
			t.Fatalf("prepare concurrent physical kernel issue %q: %v", statement, err)
		}
	}

	applicationName := "sforum-kernel-compatibility-lock"
	upgradeConfig, err := pgx.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	upgradeConfig.RuntimeParams["application_name"] = applicationName
	upgradeDB := stdlib.OpenDB(*upgradeConfig)
	defer upgradeDB.Close()
	result := make(chan error, 1)
	go func() {
		_, err := prepareCoreMigrationAuthorityForVersion(ctx, upgradeDB, "2.0.0", true)
		result <- err
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity
			  WHERE application_name = $1
			    AND position('pg_advisory_xact_lock' in query) > 0
			)
		`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("upgrade returned before the physical lock interleaving: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("upgrade did not reach the physical advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := issueTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrCoreUpgradeIncompatible) {
		t.Fatalf("locked physical compatibility recheck error = %v", err)
	}
	assertCoreAuthorityOwner(t, ctx, db, "relation", "public."+tableName, fixture.ownerRole)
}

func TestCorePhysicalAuthoritySessionSpansMigrationPipeline(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db := openCoreAuthorityFixtureDB(t, fixture)
	defer db.Close()

	prepared := make(chan struct{})
	coreFinished := make(chan struct{})
	releaseCore := make(chan struct{})
	releaseRiver := make(chan struct{})
	pipelineResult := make(chan error, 1)
	go func() {
		pipelineResult <- withCorePhysicalAuthoritySession(ctx, db, func(connection *sql.Conn) error {
			if _, err := prepareCoreMigrationAuthorityForVersion(ctx, connection, "1.0.0", true); err != nil {
				return err
			}
			close(prepared)
			select {
			case <-releaseCore:
			case <-ctx.Done():
				return ctx.Err()
			}
			close(coreFinished)
			select {
			case <-releaseRiver:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()
	select {
	case <-prepared:
	case err := <-pipelineResult:
		t.Fatalf("migration pipeline returned before preparation: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}

	applicationName := "sforum-core-physical-session-waiter"
	waiterConfig, err := pgx.ParseConfig(fixture.databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	waiterConfig.RuntimeParams["application_name"] = applicationName
	waiterDB := stdlib.OpenDB(*waiterConfig)
	defer waiterDB.Close()
	waiterTx, err := waiterDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterTx.Rollback()
	waiterResult := make(chan error, 1)
	go func() {
		err := lockCorePhysicalAuthority(ctx, waiterTx)
		if err == nil {
			err = waiterTx.Rollback()
		}
		waiterResult <- err
	}()
	waitForCorePhysicalAuthorityWait(t, ctx, db, applicationName, waiterResult)

	close(releaseCore)
	select {
	case <-coreFinished:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-waiterResult:
		t.Fatalf("physical authority changed between Core and River migrations: %v", err)
	default:
	}
	close(releaseRiver)
	if err := <-pipelineResult; err != nil {
		t.Fatalf("run locked migration pipeline: %v", err)
	}
	if err := <-waiterResult; err != nil {
		t.Fatalf("acquire physical authority after migration pipeline: %v", err)
	}
}

func TestCorePhysicalAuthoritySessionUnlocksAfterCancellation(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithCancel(context.Background())
	db := openCoreAuthorityFixtureDB(t, fixture)
	defer db.Close()

	err := withCorePhysicalAuthoritySession(ctx, db, func(*sql.Conn) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled physical authority session error = %v", err)
	}

	verifyCtx, verifyCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer verifyCancel()
	tx, err := db.BeginTx(verifyCtx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := lockCorePhysicalAuthority(verifyCtx, tx); err != nil {
		t.Fatalf("physical authority lock survived canceled migration context: %v", err)
	}
}

func TestCorePhysicalAuthoritySessionDiscardsUncertainUnlockConnection(t *testing.T) {
	fixture := newCoreAuthorityTestDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db := openCoreAuthorityFixtureDB(t, fixture)
	defer db.Close()
	killer := openCoreAuthorityFixtureDB(t, fixture)
	defer killer.Close()

	err := withCorePhysicalAuthoritySession(ctx, db, func(connection *sql.Conn) error {
		var backendPID int
		if err := connection.QueryRowContext(ctx, `SELECT pg_backend_pid()`).Scan(&backendPID); err != nil {
			return err
		}
		var terminated bool
		if err := killer.QueryRowContext(ctx, `SELECT pg_terminate_backend($1)`, backendPID).Scan(&terminated); err != nil {
			return err
		}
		if !terminated {
			return errors.New("physical authority backend was not terminated")
		}
		return nil
	})
	if err == nil {
		t.Fatal("uncertain physical authority unlock unexpectedly succeeded")
	}
	if idle := db.Stats().Idle; idle != 0 {
		t.Fatalf("uncertain physical authority connection returned to idle pool: %d", idle)
	}

	if err := withCorePhysicalAuthoritySession(ctx, db, func(*sql.Conn) error { return nil }); err != nil {
		t.Fatalf("acquire physical authority after discarded connection: %v", err)
	}
}

func waitForCorePhysicalAuthorityWait(
	t *testing.T,
	ctx context.Context,
	db *sql.DB,
	applicationName string,
	result <-chan error,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := db.QueryRowContext(ctx, `
			SELECT EXISTS (
			  SELECT 1 FROM pg_stat_activity
			  WHERE application_name = $1
			    AND position('pg_advisory_xact_lock' in query) > 0
			)
		`, applicationName).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("physical authority waiter returned before blocking: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("physical authority waiter did not reach the advisory lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
