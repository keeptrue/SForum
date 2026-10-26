package extensionsruntime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

const (
	extensionDatabaseRuntimeLeaseProcessHelperEnv   = "SFORUM_DATABASE_LEASE_PROCESS_HELPER"
	extensionDatabaseRuntimeLeaseProcessArtifactEnv = "SFORUM_DATABASE_LEASE_PROCESS_ARTIFACT"
)

func TestPostgresExtensionDatabaseRawCoreAuthorityIsExact(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	coreProbeName := createExtensionDatabaseRawCoreProbe(t, ctx, pool)
	coreProbe := pgx.Identifier{extensionDatabaseCoreSchema, coreProbeName}.Sanitize()
	coreProbeSequence := pgx.Identifier{extensionDatabaseCoreSchema, coreProbeName + "_id_seq"}.Sanitize()
	stableViewProbeName := createExtensionDatabaseStableViewProbe(t, ctx, pool)
	stableViewProbe := pgx.Identifier{extensionDatabaseCoreViewSchema, stableViewProbeName}.Sanitize()
	riverProbeName := createExtensionDatabaseRawCoreExcludedRelationProbe(t, ctx, pool)
	riverProbe := pgx.Identifier{extensionDatabaseCoreSchema, riverProbeName}.Sanitize()
	routineProbeName := createExtensionDatabaseRawCoreRoutineProbe(
		t, ctx, pool, extensionDatabaseCoreSchema, true,
	)
	routineProbe := pgx.Identifier{extensionDatabaseCoreSchema, routineProbeName}.Sanitize()
	extensionID := fmt.Sprintf("p5.raw-core.exact.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "raw-core", []string{
			extensionmanifest.DatabaseGrantOwnSchema,
			extensionmanifest.DatabaseGrantCoreViews,
			extensionmanifest.DatabaseGrantRawCore,
		},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })

	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: "raw-core-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 701, AuditEventID: 801,
		},
	})
	if err != nil {
		t.Fatalf("issue raw Core lease: %v", err)
	}
	futureRoutineProbeName := createExtensionDatabaseRawCoreRoutineProbe(
		t, ctx, pool, extensionDatabaseCoreSchema, false,
	)
	futureRoutineProbe := pgx.Identifier{extensionDatabaseCoreSchema, futureRoutineProbeName}.Sanitize()
	futureRiverRoutineProbeName := createExtensionDatabaseSessionRoutineProbe(t, ctx, pool)
	futureRiverRoutineProbe := pgx.Identifier{extensionDatabaseCoreSchema, futureRiverRoutineProbeName}.Sanitize()
	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())

	var rowID int64
	if err := connection.QueryRow(ctx, `INSERT INTO `+coreProbe+` (note) VALUES ('created') RETURNING id`).Scan(&rowID); err != nil {
		t.Fatalf("raw Core INSERT or sequence use failed: %v", err)
	}
	var note string
	if err := connection.QueryRow(ctx, `SELECT note FROM `+coreProbe+` WHERE id = $1`, rowID).Scan(&note); err != nil || note != "created" {
		t.Fatalf("raw Core SELECT: note=%q err=%v", note, err)
	}
	if _, err := connection.Exec(ctx, `UPDATE `+coreProbe+` SET note = 'updated' WHERE id = $1`, rowID); err != nil {
		t.Fatalf("raw Core UPDATE: %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM `+coreProbe+` WHERE id = $1`, rowID); err != nil {
		t.Fatalf("raw Core DELETE: %v", err)
	}
	if _, err := connection.Exec(ctx, `CREATE TABLE raw_core_own_schema_probe (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatalf("additive own_schema authority was lost: %v", err)
	}
	var stableMarker int
	if err := connection.QueryRow(ctx, `SELECT marker FROM `+stableViewProbe).Scan(&stableMarker); err != nil || stableMarker != 7 {
		t.Fatalf("additive core_views authority: marker=%d err=%v", stableMarker, err)
	}

	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `TRUNCATE TABLE `+coreProbe)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `ALTER TABLE `+coreProbe+` ADD COLUMN forbidden TEXT`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `CREATE INDEX raw_core_forbidden_idx ON `+coreProbe+` (note)`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `CREATE TABLE public.raw_core_forbidden_create (id BIGINT)`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT * FROM public.river_job`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT * FROM `+riverProbe)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+routineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+futureRoutineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+futureRiverRoutineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection,
		`CREATE TABLE raw_core_forbidden_reference (core_id BIGINT REFERENCES `+coreProbe+` (id))`,
	)
	assertExtensionDatabaseRawCoreValidationRejectsDrift(
		t, ctx, pool, credential, coreProbe, coreProbeSequence, stableViewProbe,
	)
	var hostRoutineResult string
	if err := pool.QueryRow(ctx, `SELECT `+routineProbe+`()`).Scan(&hostRoutineResult); err != nil || hostRoutineResult != "forbidden" {
		t.Fatalf("Host lost inherited Core routine execution: result=%q err=%v", hostRoutineResult, err)
	}
}

func TestPostgresExtensionDatabaseLowerTiersCannotAccessRawCore(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	coreProbeName := createExtensionDatabaseRawCoreProbe(t, ctx, pool)
	coreProbe := pgx.Identifier{extensionDatabaseCoreSchema, coreProbeName}.Sanitize()
	stableRoutineProbeName := createExtensionDatabaseRawCoreRoutineProbe(
		t, ctx, pool, extensionDatabaseCoreViewSchema, true,
	)
	stableRoutineProbe := pgx.Identifier{extensionDatabaseCoreViewSchema, stableRoutineProbeName}.Sanitize()

	tests := []struct {
		name   string
		powers []string
	}{
		{name: "own schema", powers: []string{extensionmanifest.DatabaseGrantOwnSchema}},
		{name: "core views", powers: []string{extensionmanifest.DatabaseGrantCoreViews}},
		{name: "host commands", powers: []string{extensionmanifest.DatabaseGrantHostCommands}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			extensionID := fmt.Sprintf("p5.raw-core.lower.%d.%d", time.Now().UnixNano(), index)
			artifact := insertExtensionDatabaseRuntimeLeaseFixture(
				t, ctx, pool, extensionID, "1.0.0", test.name, test.powers,
			)
			identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
			credential, err := NewPostgresExtensionDatabaseRegistry(pool, nil).IssueRuntimeLease(
				ctx,
				ExtensionDatabaseRuntimeLeaseIssue{
					Artifact: artifact, RuntimeInstanceID: "lower-runtime",
					Authority: ExtensionDatabaseLeaseAuthority{
						Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 702, AuditEventID: int64(810 + index),
					},
				},
			)
			if err != nil {
				t.Fatalf("issue lower-tier lease: %v", err)
			}
			connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close(context.Background())
			execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT * FROM `+coreProbe)
			execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `INSERT INTO `+coreProbe+` (note) VALUES ('forbidden')`)
			execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `CREATE TABLE public.lower_tier_forbidden (id BIGINT)`)
			execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+stableRoutineProbe+`()`)
		})
	}
}

func TestPostgresExtensionDatabaseRawCoreRejectsForeignPublicOwner(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("p5_raw_core_foreign_%d", time.Now().UnixNano())
	object := pgx.Identifier{extensionDatabaseCoreSchema, name}.Sanitize()
	if _, err := tx.Exec(ctx, `CREATE TABLE `+object+` (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExtensionDatabaseCoreRelations(ctx, tx); !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("foreign public relation owner error = %v", err)
	}
}

func TestPostgresExtensionDatabaseRuntimeLeaseKernelFirstIssueHardensRoutines(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	routineName := createExtensionDatabaseRawCoreRoutineProbe(
		t, ctx, pool, extensionDatabaseCoreSchema, true,
	)
	routine := pgx.Identifier{extensionDatabaseCoreSchema, routineName}.Sanitize()
	extensionID := fmt.Sprintf("p5.kernel.first-lease.%d", time.Now().UnixNano())
	artifact := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "kernel", []string{extensionmanifest.DatabaseGrantKernel},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	credential, err := NewPostgresExtensionDatabaseRegistry(pool, nil).IssueRuntimeLease(
		ctx,
		ExtensionDatabaseRuntimeLeaseIssue{
			Artifact: artifact, RuntimeInstanceID: "kernel-first-runtime",
			Authority: ExtensionDatabaseLeaseAuthority{
				Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 707, AuditEventID: 825,
			},
		},
	)
	if err != nil {
		t.Fatalf("issue first kernel lease: %v", err)
	}
	connection, err := connectExtensionDatabaseRuntimeCredential(ctx, pool, credential)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close(context.Background())
	var result string
	if err := connection.QueryRow(ctx, `SELECT `+routine+`()`).Scan(&result); err != nil || result != "forbidden" {
		t.Fatalf("kernel lost routine execution: result=%q err=%v", result, err)
	}
	var publicCanExecute bool
	if err := pool.QueryRow(ctx, `
		SELECT has_function_privilege('public', routines.oid, 'EXECUTE')
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		WHERE namespaces.nspname = $1 AND routines.proname = $2
	`, extensionDatabaseCoreSchema, routineName).Scan(&publicCanExecute); err != nil {
		t.Fatal(err)
	}
	if publicCanExecute {
		t.Fatal("kernel first issue retained PUBLIC routine execution")
	}
}

func TestPostgresExtensionDatabaseRuntimeLeasesSerializeAcrossProcesses(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	type processFixture struct {
		artifact    ExtensionDatabaseArtifact
		identifiers ExtensionDatabaseIdentifiers
	}
	fixtures := make([]processFixture, 0, 2)
	for index := range 2 {
		extensionID := fmt.Sprintf("p5.runtime.multiprocess.%d.%d", time.Now().UnixNano(), index)
		artifact := insertExtensionDatabaseRuntimeLeaseFixture(
			t, ctx, pool, extensionID, "1.0.0", fmt.Sprintf("process-%d", index),
			[]string{extensionmanifest.DatabaseGrantRawCore},
		)
		identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
		if err != nil {
			t.Fatal(err)
		}
		fixtures = append(fixtures, processFixture{artifact: artifact, identifiers: identifiers})
		t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	}

	type processResult struct {
		index  int
		output string
		err    error
	}
	start := make(chan struct{})
	results := make(chan processResult, len(fixtures))
	for index, fixture := range fixtures {
		artifactJSON, err := json.Marshal(fixture.artifact)
		if err != nil {
			t.Fatal(err)
		}
		encodedArtifact := base64.RawStdEncoding.EncodeToString(artifactJSON)
		go func(index int) {
			<-start
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExtensionDatabaseRuntimeLeaseProcessHelper$")
			command.Env = append(os.Environ(),
				extensionDatabaseRuntimeLeaseProcessHelperEnv+"=1",
				extensionDatabaseRuntimeLeaseProcessArtifactEnv+"="+encodedArtifact,
				"SFORUM_TEST_DATABASE_URL="+databaseURL,
			)
			output, runErr := command.CombinedOutput()
			results <- processResult{index: index, output: string(output), err: runErr}
		}(index)
	}
	close(start)
	for range fixtures {
		result := <-results
		if result.err != nil {
			t.Fatalf("runtime lease process %d failed: %v\n%s", result.index, result.err, result.output)
		}
	}
	var revokedLeases, liveRoles int
	if err := pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM extension_database_runtime_leases
		   WHERE extension_id = ANY($1) AND status = 'revoked'),
		  (SELECT count(*) FROM pg_roles
		   WHERE rolname IN (
		     SELECT role_name FROM extension_database_runtime_leases WHERE extension_id = ANY($1)
		   ))
	`, []string{fixtures[0].artifact.ExtensionID, fixtures[1].artifact.ExtensionID}).Scan(
		&revokedLeases, &liveRoles,
	); err != nil {
		t.Fatal(err)
	}
	if revokedLeases != len(fixtures) || liveRoles != 0 {
		t.Fatalf("multiprocess lease cleanup: revoked=%d roles=%d", revokedLeases, liveRoles)
	}
}

func TestExtensionDatabaseRuntimeLeaseProcessHelper(t *testing.T) {
	if os.Getenv(extensionDatabaseRuntimeLeaseProcessHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	artifactJSON, err := base64.RawStdEncoding.DecodeString(
		strings.TrimSpace(os.Getenv(extensionDatabaseRuntimeLeaseProcessArtifactEnv)),
	)
	if err != nil {
		t.Fatal(err)
	}
	var artifact ExtensionDatabaseArtifact
	if err := json.Unmarshal(artifactJSON, &artifact); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL")))
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: artifact, RuntimeInstanceID: fmt.Sprintf("process-%d", os.Getpid()),
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 708, AuditEventID: 826,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseRef{
		Artifact: artifact, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID,
	}, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 708, AuditEventID: 827,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresExtensionDatabaseRawCoreLeaseRejectsForeignIdentity(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	extensionID := fmt.Sprintf("p5.raw-core.identity.%d", time.Now().UnixNano())
	source := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "1.0.0", "source", []string{extensionmanifest.DatabaseGrantRawCore},
	)
	target := insertExtensionDatabaseRuntimeLeaseFixture(
		t, ctx, pool, extensionID, "2.0.0", "target", []string{extensionmanifest.DatabaseGrantRawCore},
	)
	identifiers, err := ExtensionDatabaseIdentifiersFor(extensionID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupExtensionDatabaseRuntimeLeaseFixture(t, pool, extensionID, identifiers) })
	registry := NewPostgresExtensionDatabaseRegistry(pool, nil)
	credential, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: source, RuntimeInstanceID: "source-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 703, AuditEventID: 820,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: target, RuntimeInstanceID: "foreign-grant-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost},
	}); !errors.Is(err, ErrExtensionDatabaseGrantNotFound) {
		t.Fatalf("Host issued a lease without the target exact grant: %v", err)
	}
	forgedArtifact := source
	forgedArtifact.PackageDigest = target.PackageDigest
	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: forgedArtifact, RuntimeInstanceID: "forged-artifact-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{
			Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 704, AuditEventID: 821,
		},
	}); !errors.Is(err, ErrExtensionDatabaseArtifactConflict) {
		t.Fatalf("forged exact artifact was accepted: %v", err)
	}

	validRef := ExtensionDatabaseRuntimeLeaseRef{
		Artifact: source, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID,
	}
	foreignRefs := []ExtensionDatabaseRuntimeLeaseRef{
		{Artifact: target, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: credential.LeaseID},
		{Artifact: source, RuntimeInstanceID: "foreign-runtime", LeaseID: credential.LeaseID},
		{Artifact: source, RuntimeInstanceID: credential.RuntimeInstanceID, LeaseID: strings.Repeat("f", 64)},
	}
	for _, ref := range foreignRefs {
		if _, err := registry.InspectRuntimeLease(ctx, ref); !errors.Is(err, ErrExtensionDatabaseRuntimeLeaseNotFound) {
			t.Fatalf("foreign lease identity was accepted: ref=%#v err=%v", ref, err)
		}
	}
	if _, err := pool.Exec(ctx, `
		UPDATE extension_database_grants
		SET status = 'revoked', revoked_by_user_id = 703,
		    revoke_audit_event_id = 823, revoked_at = statement_timestamp()
		WHERE id = $1
	`, credential.GrantID); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.IssueRuntimeLease(ctx, ExtensionDatabaseRuntimeLeaseIssue{
		Artifact: source, RuntimeInstanceID: "stale-grant-runtime",
		Authority: ExtensionDatabaseLeaseAuthority{Kind: ExtensionDatabaseLeaseIssuerHost},
	}); !errors.Is(err, ErrExtensionDatabaseGrantNotFound) {
		t.Fatalf("Host issued from a stale exact grant: %v", err)
	}
	if _, err := registry.RevokeRuntimeLease(ctx, validRef, ExtensionDatabaseLeaseAuthority{
		Kind: ExtensionDatabaseLeaseIssuerActor, ActorUserID: 705, AuditEventID: 822,
	}); err != nil {
		t.Fatalf("stale grant prevented exact lease cleanup: %v", err)
	}
}

func openExtensionDatabaseRawCoreTestPool(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("SFORUM_TEST_DATABASE_URL is required for raw Core integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func createExtensionDatabaseRawCoreProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) string {
	t.Helper()
	name := fmt.Sprintf("p5_raw_core_probe_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{extensionDatabaseCoreSchema, name}.Sanitize()
	execExtensionDatabaseAsCoreOwner(t, ctx, pool, `CREATE TABLE `+identifier+` (
			id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
			note TEXT NOT NULL
		)`)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP TABLE IF EXISTS `+identifier+` CASCADE`)
	})
	return name
}

func createExtensionDatabaseRawCoreExcludedRelationProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) string {
	t.Helper()
	name := fmt.Sprintf("_river_p5_probe_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{extensionDatabaseCoreSchema, name}.Sanitize()
	execExtensionDatabaseTestPhysicalMutation(t, ctx, pool, `CREATE TABLE `+identifier+` (id BIGINT PRIMARY KEY)`)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP TABLE IF EXISTS `+identifier)
	})
	return name
}

func createExtensionDatabaseStableViewProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) string {
	t.Helper()
	name := fmt.Sprintf("p5_core_view_probe_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{extensionDatabaseCoreViewSchema, name}.Sanitize()
	execExtensionDatabaseAsCoreOwner(t, ctx, pool, `CREATE VIEW `+identifier+` AS SELECT 7 AS marker`)
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP VIEW IF EXISTS `+identifier)
	})
	return name
}

func createExtensionDatabaseRawCoreRoutineProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	schemaName string,
	exposeToPublic bool,
) string {
	t.Helper()
	name := fmt.Sprintf("p5_raw_core_routine_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schemaName, name}.Sanitize()
	query := `CREATE FUNCTION ` + identifier + `() RETURNS TEXT LANGUAGE SQL AS 'SELECT ''forbidden'''`
	if exposeToPublic {
		query += `; GRANT EXECUTE ON FUNCTION ` + identifier + `() TO PUBLIC`
	}
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	ownerRole, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{ownerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	var publicCanExecute bool
	if err := tx.QueryRow(ctx, `
		SELECT has_function_privilege('public', routines.oid, 'EXECUTE')
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		WHERE namespaces.nspname = $1 AND routines.proname = $2
	`, schemaName, name).Scan(&publicCanExecute); err != nil {
		t.Fatal(err)
	}
	if publicCanExecute != exposeToPublic {
		t.Fatalf("routine PUBLIC execute = %t, want %t", publicCanExecute, exposeToPublic)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP FUNCTION IF EXISTS `+identifier+`()`)
	})
	return name
}

func createExtensionDatabaseSessionRoutineProbe(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) string {
	t.Helper()
	name := fmt.Sprintf("river_p5_routine_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{extensionDatabaseCoreSchema, name}.Sanitize()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `CREATE FUNCTION `+identifier+`() RETURNS TEXT LANGUAGE SQL AS 'SELECT ''river'''`); err != nil {
		t.Fatal(err)
	}
	var publicCanExecute bool
	if err := tx.QueryRow(ctx, `
		SELECT has_function_privilege('public', routines.oid, 'EXECUTE')
		FROM pg_proc AS routines
		JOIN pg_namespace AS namespaces ON namespaces.oid = routines.pronamespace
		WHERE namespaces.nspname = $1 AND routines.proname = $2
	`, extensionDatabaseCoreSchema, name).Scan(&publicCanExecute); err != nil {
		t.Fatal(err)
	}
	if publicCanExecute {
		t.Fatal("session-role routine default still grants PUBLIC execute")
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		execExtensionDatabaseTestPhysicalMutation(t, cleanupCtx, pool, `DROP FUNCTION IF EXISTS `+identifier+`()`)
	})
	return name
}

func execExtensionDatabaseAsCoreOwner(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
) {
	t.Helper()
	var databaseName string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&databaseName); err != nil {
		t.Fatal(err)
	}
	ownerRole, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE `+pgx.Identifier{ownerRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func execExtensionDatabaseTestPhysicalMutation(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	query string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, query); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func execExtensionDatabaseInsufficientPrivilege(
	t *testing.T,
	ctx context.Context,
	connection *pgx.Conn,
	query string,
) {
	t.Helper()
	_, err := connection.Exec(ctx, query)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("operation error = %v, want PostgreSQL insufficient_privilege", err)
	}
}

func assertExtensionDatabaseRawCoreValidationRejectsDrift(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	credential ExtensionDatabaseRuntimeCredential,
	coreProbe string,
	coreProbeSequence string,
	stableViewProbe string,
) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err := lockExtensionDatabasePhysicalAuthority(ctx, tx); err != nil {
		t.Fatal(err)
	}
	role := pgx.Identifier{credential.RoleName}.Sanitize()
	if _, err := tx.Exec(ctx, `GRANT TRUNCATE ON TABLE `+coreProbe+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE TRUNCATE, DELETE ON TABLE `+coreProbe+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `GRANT DELETE ON TABLE `+coreProbe+` TO `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT SELECT ON TABLE `+coreProbe+` TO `+role+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE GRANT OPTION FOR SELECT ON TABLE `+coreProbe+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT REFERENCES (note) ON TABLE `+coreProbe+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE REFERENCES (note) ON TABLE `+coreProbe+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT USAGE ON SEQUENCE `+coreProbeSequence+` TO `+role+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE GRANT OPTION FOR USAGE ON SEQUENCE `+coreProbeSequence+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	stableSchema := pgx.Identifier{extensionDatabaseCoreViewSchema}.Sanitize()
	if _, err := tx.Exec(ctx, `GRANT CREATE ON SCHEMA `+stableSchema+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE CREATE ON SCHEMA `+stableSchema+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT SELECT ON TABLE `+stableViewProbe+` TO `+role+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE GRANT OPTION FOR SELECT ON TABLE `+stableViewProbe+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT INSERT ON TABLE `+stableViewProbe+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE INSERT ON TABLE `+stableViewProbe+` FROM `+role); err != nil {
		t.Fatal(err)
	}

	owner := pgx.Identifier{credential.OwnerRoleName}.Sanitize()
	for _, query := range []struct {
		grant  string
		revoke string
	}{
		{`GRANT SELECT ON TABLE ` + coreProbe + ` TO ` + owner, `REVOKE SELECT ON TABLE ` + coreProbe + ` FROM ` + owner},
		{`GRANT TRUNCATE ON TABLE ` + coreProbe + ` TO ` + owner, `REVOKE TRUNCATE ON TABLE ` + coreProbe + ` FROM ` + owner},
		{`GRANT USAGE ON SEQUENCE ` + coreProbeSequence + ` TO ` + owner, `REVOKE USAGE ON SEQUENCE ` + coreProbeSequence + ` FROM ` + owner},
		{`GRANT USAGE ON SCHEMA ` + stableSchema + ` TO ` + owner, `REVOKE USAGE ON SCHEMA ` + stableSchema + ` FROM ` + owner},
		{`GRANT SELECT ON TABLE ` + stableViewProbe + ` TO ` + owner, `REVOKE SELECT ON TABLE ` + stableViewProbe + ` FROM ` + owner},
	} {
		if _, err := tx.Exec(ctx, query.grant); err != nil {
			t.Fatal(err)
		}
		assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
		if _, err := tx.Exec(ctx, query.revoke); err != nil {
			t.Fatal(err)
		}
	}
}

func assertExtensionDatabaseRawCoreValidationConflict(
	t *testing.T,
	ctx context.Context,
	tx pgx.Tx,
	credential ExtensionDatabaseRuntimeCredential,
) {
	t.Helper()
	err := validateExtensionDatabaseRawCoreAuthority(
		ctx, tx, credential.RoleName, credential.Powers,
	)
	if err == nil {
		err = validateExtensionDatabaseRawCoreAuthority(ctx, tx, credential.OwnerRoleName, nil)
	}
	if !errors.Is(err, ErrExtensionDatabaseResourceConflict) {
		t.Fatalf("raw Core ACL drift error = %v", err)
	}
}
