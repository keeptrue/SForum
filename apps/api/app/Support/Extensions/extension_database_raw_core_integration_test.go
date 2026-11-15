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
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	extensionDatabaseRuntimeLeaseProcessHelperEnv   = "SFORUM_DATABASE_LEASE_PROCESS_HELPER"
	extensionDatabaseRuntimeLeaseProcessArtifactEnv = "SFORUM_DATABASE_LEASE_PROCESS_ARTIFACT"
)

// rawCoreSensitiveTables 是「raw_core 必须零 DML」的敏感表清单（EX2-02 的 denied
// 面）。它不在授权白名单里只是必要条件；这里逐表实测 42501，才算真的不可写。
var rawCoreSensitiveTables = []string{
	"users",
	"user_credentials",
	"user_sessions",
	"api_tokens",
	"password_reset_tokens",
	"email_verification_tokens",
	"secret_store",
	"secret_store_audit",
	"user_roles",
	"role_permissions",
	"permissions",
	"user_permission_overrides",
	"identity_session_policy_selection",
	"identity_session_policy_selection_events",
	"identity_session_policy_step_up_evidence",
	"audit_events",
	"privacy_operation_audit",
	"extension_trust_grants",
	"extension_trust_challenges",
	"extension_frontend_trust_grants",
	"extension_dev_trust_sessions",
	"extension_versions",
	"extension_database_credentials",
	"extension_database_grants",
	"extension_database_grant_powers",
	"extension_permission_role_grants",
}

func TestPostgresExtensionDatabaseRawCoreAuthorityIsExact(t *testing.T) {
	ctx, pool := openExtensionDatabaseRawCoreTestPool(t)
	stableViewProbeName := createExtensionDatabaseStableViewProbe(t, ctx, pool)
	stableViewProbe := pgx.Identifier{extensionDatabaseCoreViewSchema, stableViewProbeName}.Sanitize()
	riverProbeName := createExtensionDatabaseRawCoreExcludedRelationProbe(t, ctx, pool)
	riverProbe := pgx.Identifier{extensionDatabaseCoreSchema, riverProbeName}.Sanitize()
	// 一个不属于白名单的普通 public 表：它证明「建在 public」本身不再换来 DML。
	nonAllowlistedProbeName := createExtensionDatabaseRawCoreProbe(t, ctx, pool)
	nonAllowlistedProbe := pgx.Identifier{extensionDatabaseCoreSchema, nonAllowlistedProbeName}.Sanitize()
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

	// allowed：白名单里的派生投影表可以正常读写，且 INSERT 能用上序列。
	var runID int64
	if err := connection.QueryRow(ctx,
		`INSERT INTO public.search_reindex_runs (status) VALUES ('running') RETURNING id`,
	).Scan(&runID); err != nil {
		t.Fatalf("raw Core INSERT on an allowlisted table or its sequence failed: %v", err)
	}
	if _, err := connection.Exec(ctx,
		`UPDATE public.search_reindex_runs SET status = 'completed' WHERE id = $1`, runID,
	); err != nil {
		t.Fatalf("raw Core UPDATE on an allowlisted table: %v", err)
	}
	var runStatus string
	if err := connection.QueryRow(ctx,
		`SELECT status FROM public.search_reindex_runs WHERE id = $1`, runID,
	).Scan(&runStatus); err != nil || runStatus != "completed" {
		t.Fatalf("raw Core SELECT on an allowlisted table: status=%q err=%v", runStatus, err)
	}
	if _, err := connection.Exec(ctx,
		`DELETE FROM public.search_reindex_runs WHERE id = $1`, runID,
	); err != nil {
		t.Fatalf("raw Core DELETE on an allowlisted table: %v", err)
	}
	if _, err := connection.Exec(ctx,
		`INSERT INTO public.search_documents (topic_id) VALUES (9001)`,
	); err != nil {
		t.Fatalf("raw Core INSERT on search_documents: %v", err)
	}
	if _, err := connection.Exec(ctx,
		`UPDATE public.search_documents SET title = 'rewritten' WHERE topic_id = 9001`,
	); err != nil {
		t.Fatalf("raw Core UPDATE on search_documents: %v", err)
	}
	if _, err := connection.Exec(ctx, `DELETE FROM public.search_documents WHERE topic_id = 9001`); err != nil {
		t.Fatalf("raw Core DELETE on search_documents: %v", err)
	}
	if _, err := connection.Exec(ctx, `
		INSERT INTO public.search_index_state (provider_id, topic_id, source_updated_at)
		VALUES ('p5', 9001, now())
	`); err != nil {
		t.Fatalf("raw Core INSERT on search_index_state: %v", err)
	}
	if _, err := connection.Exec(ctx,
		`DELETE FROM public.search_index_state WHERE provider_id = 'p5' AND topic_id = 9001`,
	); err != nil {
		t.Fatalf("raw Core DELETE on search_index_state: %v", err)
	}
	if _, err := connection.Exec(ctx, `CREATE TABLE raw_core_own_schema_probe (id BIGINT PRIMARY KEY)`); err != nil {
		t.Fatalf("additive own_schema authority was lost: %v", err)
	}
	var stableMarker int
	if err := connection.QueryRow(ctx, `SELECT marker FROM `+stableViewProbe).Scan(&stableMarker); err != nil || stableMarker != 7 {
		t.Fatalf("additive core_views authority: marker=%d err=%v", stableMarker, err)
	}

	// denied：敏感表逐表实测不可 DML，并且错误必须点出表名（可读）。
	for _, table := range rawCoreSensitiveTables {
		object := pgx.Identifier{extensionDatabaseCoreSchema, table}.Sanitize()
		column := pgx.Identifier{extensionDatabaseFirstColumn(t, ctx, pool, table)}.Sanitize()
		for _, statement := range []string{
			`INSERT INTO ` + object + ` DEFAULT VALUES`,
			`UPDATE ` + object + ` SET ` + column + ` = ` + column + ` WHERE false`,
			`DELETE FROM ` + object + ` WHERE false`,
		} {
			assertExtensionDatabaseDeniedStatement(t, ctx, connection, statement, "permission denied for table "+table)
		}
	}
	// denied：默认拒绝——内容表与控制表也不在白名单里。
	for _, table := range []string{"topics", "posts", "comments", "web_options", "extension_settings"} {
		object := pgx.Identifier{extensionDatabaseCoreSchema, table}.Sanitize()
		assertExtensionDatabaseDeniedStatement(t, ctx, connection,
			`INSERT INTO `+object+` DEFAULT VALUES`, "permission denied for table "+table,
		)
	}
	// denied：public schema 里的任意新表都不再自动获得 DML。
	assertExtensionDatabaseDeniedStatement(t, ctx, connection,
		`INSERT INTO `+nonAllowlistedProbe+` (note) VALUES ('forbidden')`,
		"permission denied for table "+nonAllowlistedProbeName,
	)

	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `TRUNCATE TABLE public.search_reindex_runs`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection,
		`ALTER TABLE public.search_reindex_runs ADD COLUMN forbidden TEXT`,
	)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection,
		`CREATE INDEX raw_core_forbidden_idx ON public.search_reindex_runs (status)`,
	)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `CREATE TABLE public.raw_core_forbidden_create (id BIGINT)`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT * FROM public.river_job`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT * FROM `+riverProbe)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+routineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+futureRoutineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection, `SELECT `+futureRiverRoutineProbe+`()`)
	execExtensionDatabaseInsufficientPrivilege(t, ctx, connection,
		`CREATE TABLE raw_core_forbidden_reference (core_id BIGINT REFERENCES public.search_documents (topic_id))`,
	)
	assertExtensionDatabaseRawCoreValidationRejectsDrift(
		t, ctx, pool, credential, stableViewProbe,
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
	databaseURL := testdb.IsolatedURL(t)
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
	pool, err := pgxpool.New(ctx, testdb.RequireIsolatedURL(t))
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
	databaseURL := testdb.IsolatedURL(t)
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

// extensionDatabaseFirstColumn 取表的第一列：denied 用例需要一个对任何表都合法的
// 自赋值列，硬编码列名会在没有 id 的表上先撞 42703 而不是 42501。
func extensionDatabaseFirstColumn(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	table string,
) string {
	t.Helper()
	var column string
	if err := pool.QueryRow(ctx, `
		SELECT column_name
		FROM information_schema.columns
		WHERE table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position
		LIMIT 1
	`, extensionDatabaseCoreSchema, table).Scan(&column); err != nil {
		t.Fatalf("resolve first column of %s: %v", table, err)
	}
	return column
}

// assertExtensionDatabaseDeniedStatement 断言一条写语句被 PostgreSQL 以
// insufficient_privilege 拒绝，并且错误信息点出具体对象（可读性验收项）。
func assertExtensionDatabaseDeniedStatement(
	t *testing.T,
	ctx context.Context,
	connection *pgx.Conn,
	statement string,
	wantMessage string,
) {
	t.Helper()
	_, err := connection.Exec(ctx, statement)
	var postgresError *pgconn.PgError
	if !errors.As(err, &postgresError) || postgresError.Code != "42501" {
		t.Fatalf("%s: error = %v, want PostgreSQL insufficient_privilege", statement, err)
	}
	if !strings.Contains(postgresError.Message, wantMessage) {
		t.Fatalf("%s: error message %q does not contain %q", statement, postgresError.Message, wantMessage)
	}
}

func assertExtensionDatabaseRawCoreValidationRejectsDrift(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	credential ExtensionDatabaseRuntimeCredential,
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
	allowlisted := pgx.Identifier{extensionDatabaseCoreSchema, "search_reindex_runs"}.Sanitize()
	allowlistedSequence := pgx.Identifier{extensionDatabaseCoreSchema, "search_reindex_runs_id_seq"}.Sanitize()
	sensitive := pgx.Identifier{extensionDatabaseCoreSchema, "users"}.Sanitize()

	// 白名单表少一项权限 → 冲突（白名单是精确契约，不是下限）。
	if _, err := tx.Exec(ctx, `REVOKE DELETE ON TABLE `+allowlisted+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `GRANT DELETE ON TABLE `+allowlisted+` TO `+role); err != nil {
		t.Fatal(err)
	}
	// 多一项权限 → 冲突。
	if _, err := tx.Exec(ctx, `GRANT TRUNCATE ON TABLE `+allowlisted+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE TRUNCATE ON TABLE `+allowlisted+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT SELECT ON TABLE `+allowlisted+` TO `+role+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE GRANT OPTION FOR SELECT ON TABLE `+allowlisted+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT REFERENCES (status) ON TABLE `+allowlisted+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE REFERENCES (status) ON TABLE `+allowlisted+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `GRANT USAGE ON SEQUENCE `+allowlistedSequence+` TO `+role+` WITH GRANT OPTION`); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE GRANT OPTION FOR USAGE ON SEQUENCE `+allowlistedSequence+` FROM `+role); err != nil {
		t.Fatal(err)
	}
	// 敏感表永远零权限：哪怕多授一条 INSERT 也必须被判冲突（EX2-02 的棘轮）。
	if _, err := tx.Exec(ctx, `GRANT INSERT ON TABLE `+sensitive+` TO `+role); err != nil {
		t.Fatal(err)
	}
	assertExtensionDatabaseRawCoreValidationConflict(t, ctx, tx, credential)
	if _, err := tx.Exec(ctx, `REVOKE INSERT ON TABLE `+sensitive+` FROM `+role); err != nil {
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

	// 持久 owner 不得携带任何 Core 权限——包括白名单表本身。
	owner := pgx.Identifier{credential.OwnerRoleName}.Sanitize()
	for _, query := range []struct {
		grant  string
		revoke string
	}{
		{`GRANT SELECT ON TABLE ` + allowlisted + ` TO ` + owner, `REVOKE SELECT ON TABLE ` + allowlisted + ` FROM ` + owner},
		{`GRANT DELETE ON TABLE ` + allowlisted + ` TO ` + owner, `REVOKE DELETE ON TABLE ` + allowlisted + ` FROM ` + owner},
		{`GRANT TRUNCATE ON TABLE ` + allowlisted + ` TO ` + owner, `REVOKE TRUNCATE ON TABLE ` + allowlisted + ` FROM ` + owner},
		{`GRANT USAGE ON SEQUENCE ` + allowlistedSequence + ` TO ` + owner, `REVOKE USAGE ON SEQUENCE ` + allowlistedSequence + ` FROM ` + owner},
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
