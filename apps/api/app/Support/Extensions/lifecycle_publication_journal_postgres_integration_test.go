package extensionsruntime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
)

func TestPostgresLifecyclePublicationJournalConcurrentCommitBindsOneRevision(t *testing.T) {
	fixture := newLifecyclePublicationIntegrationFixture(t)
	ctx, pool, journal, request := fixture.ctx, fixture.pool, fixture.journal, fixture.request

	committed, err := journal.LifecyclePublicationCommitted(ctx, request, LifecycleBoundaryActivate)
	if err != nil || committed {
		t.Fatalf("unprepared publication = %v, %v", committed, err)
	}
	missing := request
	missing.StepID += ".missing"
	if err := journal.CommitLifecyclePublication(ctx, missing, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalNotPrepared) {
		t.Fatalf("unprepared commit error = %v", err)
	}

	seed := seedLifecycleRuntimePublication(t, fixture, request, LifecycleBoundaryActivate)
	if err := journal.PrepareLifecyclePublication(ctx, request, LifecycleBoundaryActivate); err != nil {
		t.Fatal(err)
	}
	restarted := NewPostgresLifecycleBoundaryPublicationJournal(pool)
	rebound := request
	rebound.TargetBinding.RuntimeInstanceID = "restarted-target-runtime"
	if err := restarted.PrepareLifecyclePublication(ctx, rebound, LifecycleBoundaryActivate); err != nil {
		t.Fatalf("same-attempt runtime rebind: %v", err)
	}
	if _, err := restarted.LifecyclePublicationCommitted(ctx, request, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("superseded runtime inspection error = %v", err)
	}

	next := rebound
	next.Attempt++
	next.TargetBinding.RuntimeInstanceID = "later-attempt-target-runtime"
	if err := restarted.PrepareLifecyclePublication(ctx, next, LifecycleBoundaryActivate); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.LifecyclePublicationCommitted(ctx, rebound, LifecycleBoundaryActivate); !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("stale strict inspection error = %v", err)
	}
	early := next
	early.Attempt += 20
	early.TargetBinding.RuntimeInstanceID = "fresh-host-target-runtime"
	committed, err = restarted.LifecyclePublicationCommittedForOperation(ctx, early, LifecycleBoundaryActivate)
	if err != nil || committed {
		t.Fatalf("early operation inspection = %v, %v", committed, err)
	}

	const workers = 24
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- restarted.CommitLifecyclePublication(ctx, next, LifecycleBoundaryActivate)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	afterCommitRestart := NewPostgresLifecycleBoundaryPublicationJournal(pool)
	committed, err = afterCommitRestart.LifecyclePublicationCommitted(ctx, next, LifecycleBoundaryActivate)
	if err != nil || !committed {
		t.Fatalf("committed publication after restart = %v, %v", committed, err)
	}
	committed, err = afterCommitRestart.LifecyclePublicationCommittedForOperation(ctx, early, LifecycleBoundaryActivate)
	if err != nil || !committed {
		t.Fatalf("committed operation marker = %v, %v", committed, err)
	}

	var firstAttempt, lastAttempt, committedAttempt, runtimeAttempts int
	var marker bool
	var boundRevision int64
	if err := pool.QueryRow(ctx, `
		SELECT first_attempt, last_attempt, committed_attempt, commit_marker,
		       jsonb_array_length(runtime_attempts), plugin_runtime_publication_revision
		FROM extension_lifecycle_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = 'activate'
	`, request.OperationID, request.StepID).Scan(
		&firstAttempt, &lastAttempt, &committedAttempt, &marker, &runtimeAttempts, &boundRevision,
	); err != nil {
		t.Fatal(err)
	}
	if firstAttempt != request.Attempt || lastAttempt != next.Attempt ||
		committedAttempt != next.Attempt || !marker || runtimeAttempts != 3 ||
		boundRevision <= seed.Revision {
		t.Fatalf(
			"attempt marker = first:%d last:%d committed:%d marker:%v runtime attempts:%d revision:%d",
			firstAttempt, lastAttempt, committedAttempt, marker, runtimeAttempts, boundRevision,
		)
	}

	var reason, digest, extensionID, version, packageDigest string
	var actorUserID, versionID int64
	var memberCount int
	if err := pool.QueryRow(ctx, `
		SELECT p.reason, p.actor_user_id, p.member_count, p.members_digest,
		       m.extension_id, m.extension_version_id, m.extension_version, m.package_digest
		FROM plugin_runtime_publications AS p
		JOIN plugin_runtime_publication_members AS m
		  ON m.publication_revision = p.revision
		WHERE p.revision = $1
	`, boundRevision).Scan(
		&reason, &actorUserID, &memberCount, &digest,
		&extensionID, &versionID, &version, &packageDigest,
	); err != nil {
		t.Fatal(err)
	}
	if reason != string(extensions.PluginRuntimePublicationEnable) ||
		actorUserID != request.ActorUserID || memberCount != 1 ||
		extensionID != request.TargetExtension.ID ||
		versionID != request.TargetExtension.ActiveVersionID ||
		version != request.TargetExtension.Version ||
		packageDigest != request.TargetExtension.PackageDigest {
		t.Fatalf("bound publication = reason:%s actor:%d count:%d member:%s/%d/%s/%s",
			reason, actorUserID, memberCount, extensionID, versionID, version, packageDigest)
	}
	if digest != seed.MembersDigest {
		t.Fatalf("same full-set digest changed: seed=%s lifecycle=%s", seed.MembersDigest, digest)
	}
	assertLifecycleRuntimePublicationCount(t, fixture, 2)
	if err := afterCommitRestart.CommitLifecyclePublication(ctx, next, LifecycleBoundaryActivate); err != nil {
		t.Fatalf("idempotent commit replay: %v", err)
	}
	assertLifecycleRuntimePublicationCount(t, fixture, 2)

	if _, err := pool.Exec(ctx, `DELETE FROM extensions WHERE id = $1`, fixture.extensionID); err == nil {
		t.Fatal("published exact version was physically deleted")
	}
	committed, err = afterCommitRestart.LifecyclePublicationCommitted(ctx, next, LifecycleBoundaryActivate)
	if err != nil || !committed {
		t.Fatalf("publication after rejected extension deletion = %v, %v", committed, err)
	}
}

func TestPostgresLifecyclePublicationCommitRejectsGrantRevokedAfterStaging(t *testing.T) {
	fixture := newLifecyclePublicationIntegrationFixture(t)
	request := fixture.request
	var grantID int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		INSERT INTO extension_trust_grants (
			extension_id, extension_version, package_digest, action
		) VALUES ($1, $2, $3, $4)
		RETURNING id
	`, request.TargetExtension.ID, request.TargetExtension.Version,
		request.TargetExtension.PackageDigest, extensions.TrustActionEnable).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_lifecycle_operations
		SET authority_type = $2, trust_grant_id = $3,
			authority_snapshot = $4::jsonb
		WHERE id = $1
	`, request.OperationID, extensions.LifecycleAuthorityTrustGrant, grantID,
		`{"schemaVersion":"sforum.lifecycle.authority@1"}`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.journal.PrepareLifecyclePublication(
		fixture.ctx, request, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatal(err)
	}

	revokeTx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer revokeTx.Rollback(context.Background())
	if err := extensions.LockExecutableTrustExtensionTx(
		fixture.ctx, revokeTx, request.TargetExtension.ID,
	); err != nil {
		t.Fatal(err)
	}
	var revokePID int32
	if err := revokeTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&revokePID); err != nil {
		t.Fatal(err)
	}
	if _, err := revokeTx.Exec(fixture.ctx, `
		UPDATE extension_trust_grants SET revoked_at = statement_timestamp() WHERE id = $1
	`, grantID); err != nil {
		t.Fatal(err)
	}

	waiterApplicationName := "lpj_trust_waiter_" + fixture.schema
	waiterConfig := fixture.pool.Config().Copy()
	waiterConfig.ConnConfig.RuntimeParams["application_name"] = waiterApplicationName
	waiterConfig.MaxConns = 1
	waiterPool, err := pgxpool.NewWithConfig(fixture.ctx, waiterConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer waiterPool.Close()
	waiterJournal := NewPostgresLifecycleBoundaryPublicationJournal(waiterPool)
	result := make(chan error, 1)
	go func() {
		result <- waiterJournal.CommitLifecyclePublication(
			fixture.ctx, request, LifecycleBoundaryActivate,
		)
	}()
	waitForLifecyclePublicationTrustAdvisoryLockWaiter(
		t, fixture.ctx, fixture.pool, revokePID, waiterApplicationName, result,
	)
	if err := revokeTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("revoked activation commit error=%v", err)
	}
	assertLifecycleRuntimePublicationCount(t, fixture, 0)
	var committed bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT commit_marker
		FROM extension_lifecycle_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = 'activate'
	`, request.OperationID, request.StepID).Scan(&committed); err != nil {
		t.Fatal(err)
	}
	if committed {
		t.Fatal("revoked activation retained a durable commit marker")
	}
}

func waitForLifecyclePublicationTrustAdvisoryLockWaiter(
	t *testing.T,
	ctx context.Context,
	observer *pgxpool.Pool,
	blockerPID int32,
	applicationName string,
	result <-chan error,
) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()

	for {
		var waiting bool
		if err := observer.QueryRow(waitCtx, `
			SELECT EXISTS (
				SELECT 1
				FROM pg_locks AS held
				JOIN pg_locks AS waiter
				  ON waiter.locktype = held.locktype
				 AND waiter.database IS NOT DISTINCT FROM held.database
				 AND waiter.classid IS NOT DISTINCT FROM held.classid
				 AND waiter.objid IS NOT DISTINCT FROM held.objid
				 AND waiter.objsubid IS NOT DISTINCT FROM held.objsubid
				JOIN pg_stat_activity AS activity ON activity.pid = waiter.pid
				WHERE held.pid = $1
				  AND held.locktype = 'advisory'
				  AND held.granted
				  AND NOT waiter.granted
				  AND activity.application_name = $2
				  AND $1 = ANY(pg_blocking_pids(waiter.pid))
			)
		`, blockerPID, applicationName).Scan(&waiting); err != nil {
			t.Fatalf("inspect lifecycle publication trust-lock waiter: %v", err)
		}
		if waiting {
			return
		}
		select {
		case err := <-result:
			t.Fatalf("lifecycle publication returned before reaching the trust lock: %v", err)
		case <-waitCtx.Done():
			t.Fatalf("lifecycle publication did not reach the trust lock: %v", waitCtx.Err())
		case <-ticker.C:
		}
	}
}

func TestPostgresLifecyclePublicationJournalMarkerCASRollbackDropsRevision(t *testing.T) {
	fixture := newLifecyclePublicationIntegrationFixture(t)
	if err := fixture.journal.PrepareLifecyclePublication(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		CREATE FUNCTION test_skip_lifecycle_marker_update() RETURNS trigger
		LANGUAGE plpgsql AS $$
		BEGIN
		  RETURN NULL;
		END;
		$$;
		CREATE TRIGGER zz_test_skip_lifecycle_marker_update
		BEFORE UPDATE OF commit_marker ON extension_lifecycle_publications
		FOR EACH ROW
		WHEN (OLD.commit_marker IS FALSE AND NEW.commit_marker IS TRUE)
		EXECUTE FUNCTION test_skip_lifecycle_marker_update();
	`); err != nil {
		t.Fatal(err)
	}

	err := fixture.journal.CommitLifecyclePublication(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	)
	if !errors.Is(err, ErrLifecyclePublicationJournalConflict) {
		t.Fatalf("zero-row marker update error = %v", err)
	}
	assertLifecycleRuntimePublicationCount(t, fixture, 0)
	var marker bool
	var binding sql.NullInt64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT commit_marker, plugin_runtime_publication_revision
		FROM extension_lifecycle_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = 'activate'
	`, fixture.request.OperationID, fixture.request.StepID).Scan(&marker, &binding); err != nil {
		t.Fatal(err)
	}
	if marker || binding.Valid {
		t.Fatalf("rolled back marker = %v, binding = %#v", marker, binding)
	}
}

func TestPostgresLifecyclePublicationJournalCommitUnknownIsInspectable(t *testing.T) {
	fixture := newLifecyclePublicationIntegrationFixture(t)
	if err := fixture.journal.PrepareLifecyclePublication(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatal(err)
	}
	// 模拟服务端已成功提交，但调用端在处理返回值前丢失响应。恢复只检查新连接
	// 可见的 durable marker/binding，不通过裸 UPDATE 制造不可能的新历史。
	if err := fixture.journal.CommitLifecyclePublication(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatal(err)
	}

	restarted := NewPostgresLifecycleBoundaryPublicationJournal(fixture.pool)
	committed, err := restarted.LifecyclePublicationCommitted(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	)
	if err != nil || !committed {
		t.Fatalf("inspect unknown commit = %v, %v", committed, err)
	}
	var binding int64
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT plugin_runtime_publication_revision
		FROM extension_lifecycle_publications
		WHERE operation_id = $1 AND step_id = $2 AND publication_mode = 'activate'
	`, fixture.request.OperationID, fixture.request.StepID).Scan(&binding); err != nil {
		t.Fatal(err)
	}
	if binding <= 0 {
		t.Fatalf("unknown commit binding = %d", binding)
	}
	if err := restarted.CommitLifecyclePublication(
		fixture.ctx, fixture.request, LifecycleBoundaryActivate,
	); err != nil {
		t.Fatalf("idempotent unknown commit replay: %v", err)
	}
	assertLifecycleRuntimePublicationCount(t, fixture, 1)
}

type lifecyclePublicationIntegration struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	journal     *PostgresLifecycleBoundaryPublicationJournal
	request     LifecycleBoundaryRequest
	extensionID string
	schema      string
}

func newLifecyclePublicationIntegration(
	t *testing.T,
) (context.Context, *pgxpool.Pool, *PostgresLifecycleBoundaryPublicationJournal, LifecycleBoundaryRequest, string) {
	t.Helper()
	fixture := newLifecyclePublicationIntegrationFixture(t)
	return fixture.ctx, fixture.pool, fixture.journal, fixture.request, fixture.extensionID
}

func TestLifecyclePublicationIntegrationRequiresExplicitTestDatabaseURL(t *testing.T) {
	t.Setenv("SFORUM_TEST_DATABASE_URL", "")
	t.Setenv("DATABASE_URL", "postgres://should-never-be-used/for-integration")
	if url, ok := requireSforumTestDatabaseURL(); ok || url != "" {
		t.Fatalf("setup must refuse without SFORUM_TEST_DATABASE_URL; got url=%q ok=%v", url, ok)
	}
}

func newLifecyclePublicationIntegrationFixture(t *testing.T) *lifecyclePublicationIntegration {
	t.Helper()
	databaseURL, ok := requireSforumTestDatabaseURL()
	if !ok {
		t.Skip("SFORUM_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	// 唯一 schema 前缀避免 -count 重跑时截断冲突；操作行也使用带时间戳 extension id。
	schema := fmt.Sprintf("lpj_%d", time.Now().UnixNano())
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	var db *sql.DB
	// Cleanup 顺序必须先关 pool/db（释放 search_path 连接），再 DROP SCHEMA。
	// 不在 cleanup 前 defer pool.Close；DROP 失败必须可见，不能静默忽略。
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
			pool = nil
		}
		if db != nil {
			if err := db.Close(); err != nil {
				t.Errorf("close lifecycle publication migration db: %v", err)
			}
			db = nil
		}
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop lifecycle publication private schema %s: %v", schema, err)
		}
		admin.Close()
	})

	config, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["search_path"] = schema + ",public"
	db = stdlib.OpenDB(*config)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.ExecContext(ctx, `
		CREATE TABLE users (
			id BIGSERIAL PRIMARY KEY,
			username TEXT NOT NULL,
			username_lower TEXT NOT NULL UNIQUE,
			email TEXT NOT NULL,
			email_lower TEXT NOT NULL UNIQUE,
			display_name TEXT NOT NULL
		);
		CREATE TABLE extension_trust_grants (
			id BIGSERIAL PRIMARY KEY,
			extension_id TEXT NOT NULL DEFAULT '',
			extension_version TEXT NOT NULL DEFAULT '',
			package_digest TEXT NOT NULL DEFAULT repeat('0', 64),
			action TEXT NOT NULL DEFAULT 'enable',
			revoked_at TIMESTAMPTZ
		);
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL CHECK (type IN ('plugin', 'theme')),
			name TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed',
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			package_digest TEXT NOT NULL,
			manifest JSONB NOT NULL
		);
		ALTER TABLE extensions
		  ADD CONSTRAINT extensions_active_version_fk
		  FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
	`); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres, db, migrations.Files(), goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	// 含 job/registry publications：jobs 与 registry 集成测试复用本 fixture，
	// 避免 search_path 回落到 public 宿主表。
	// 202607140004 将 host.gate 加入 lifecycle_action check；jobs 等 fixture
	// 会写入 host.gate step，缺少该迁移会在 setup 阶段 SQLSTATE 23514 失败。
	for _, version := range []int64{
		202607140001,
		202607140004,
		202607140007,
		202607140010,
		202607140011,
		202607160027,
		202607160030,
		202607160031,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db = nil

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}

	// 确认 lifecycle 账本在私有 schema，失败路径也不会把 planned 操作写进宿主 public。
	var operationsNamespace string
	if err := pool.QueryRow(ctx, `
		SELECT n.nspname
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = 'extension_lifecycle_operations'
		  AND n.nspname = current_schema()
	`).Scan(&operationsNamespace); err != nil {
		t.Fatalf("lifecycle operations table is not in private schema: %v", err)
	}
	if operationsNamespace != schema {
		t.Fatalf("lifecycle operations schema = %q, want %q", operationsNamespace, schema)
	}

	request := lifecyclePublicationTestRequest(t, extensions.LifecycleMachineEnable, 5)
	extensionID := fmt.Sprintf("publication.integration.%d", time.Now().UnixNano())
	request.TargetExtension.ID = extensionID
	request.TargetExtension.Manifest.ID = extensionID
	request.TargetExtension.Manifest.PackageFiles[0].ID = extensionID + ".backend"
	request.TargetBinding.ExtensionID = extensionID
	source := request.TargetExtension
	request.SourceExtension = &source
	request.SourceBinding = request.TargetBinding
	if err := extensionmanifest.Validate(request.TargetExtension.Manifest); err != nil {
		t.Fatalf("integration manifest: %v", err)
	}
	manifest, err := json.Marshal(request.TargetExtension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest) == 0 || string(manifest) == "null" || string(manifest) == "{}" {
		t.Fatal("enabled publication fixture requires a non-empty exact manifest")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		INSERT INTO extensions (id, type, name, status)
		VALUES ($1, 'plugin', 'Publication Integration', 'enabled')
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO extension_versions (
			id, extension_id, version, package_digest, manifest
		) VALUES ($1, $2, $3, $4, $5::jsonb)
	`, request.TargetExtension.ActiveVersionID, extensionID,
		request.TargetExtension.Version, request.TargetExtension.PackageDigest, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE extensions SET active_version_id = $2 WHERE id = $1
	`, extensionID, request.TargetExtension.ActiveVersionID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO extension_lifecycle_operations (
			extension_id, extension_version, package_digest, artifact_digests,
			operation, plan_version, idempotency_key, request_fingerprint,
			authority_type, authority_snapshot
		) VALUES ($1, $2, $3, '{}'::jsonb, 'enable', 'publication.integration@1',
		          $4, $5, 'builtin', '{}'::jsonb)
		RETURNING id
	`, extensionID, request.TargetExtension.Version, request.TargetExtension.PackageDigest,
		"publication:"+extensionID, strings.Repeat("c", 64)).Scan(&request.OperationID); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	return &lifecyclePublicationIntegration{
		ctx: ctx, pool: pool,
		journal: NewPostgresLifecycleBoundaryPublicationJournal(pool),
		request: request, extensionID: extensionID, schema: schema,
	}
}

func seedLifecycleRuntimePublication(
	t *testing.T,
	fixture *lifecyclePublicationIntegration,
	request LifecycleBoundaryRequest,
	mode LifecycleBoundaryPublicationMode,
) extensions.PluginRuntimePublication {
	t.Helper()
	transition, err := lifecyclePluginRuntimePublicationTransition(request, mode)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(fixture.ctx)
	publication, err := extensions.PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, tx, transition,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}
	return publication
}

func assertLifecycleRuntimePublicationCount(
	t *testing.T,
	fixture *lifecyclePublicationIntegration,
	want int,
) {
	t.Helper()
	var got int
	if err := fixture.pool.QueryRow(
		fixture.ctx, `SELECT count(*) FROM plugin_runtime_publications`,
	).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("plugin runtime publication count = %d, want %d", got, want)
	}
}
