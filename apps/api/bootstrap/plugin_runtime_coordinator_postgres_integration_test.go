package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	"github.com/keeptrue/sforum/apps/api/database/migrations"
	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	pluginRuntimeCoordinatorLifecycleMigration = int64(202607140001)
	pluginRuntimeCoordinatorRecoveryMigration  = int64(202607140006)
	pluginRuntimeCoordinatorGenesisMigration   = int64(202607160027)
)

type pluginRuntimeCoordinatorPostgresFixture struct {
	ctx         context.Context
	pool        *pgxpool.Pool
	store       *extensions.PostgresStore
	repository  *extensions.PostgresLifecycleRepository
	schema      string
	extensionID string
}

type recordingPluginRuntimeCoordinatorPostgresEnsurer struct {
	delegate extensions.InitialPluginRuntimePublicationEnsurer
	calls    atomic.Int32
	first    chan error
}

func (ensurer *recordingPluginRuntimeCoordinatorPostgresEnsurer) EnsureInitialPluginRuntimePublication(
	ctx context.Context,
) (extensions.PluginRuntimePublication, error) {
	publication, err := ensurer.delegate.EnsureInitialPluginRuntimePublication(ctx)
	if ensurer.calls.Add(1) == 1 {
		ensurer.first <- err
	}
	return publication, err
}

func TestPluginRuntimeCoordinatorPostgresGenesisWaitsForLifecycleCompletion(t *testing.T) {
	fixture := newPluginRuntimeCoordinatorPostgresFixture(t, "wait")
	operation := fixture.acquireOpenLifecycle(t)
	ensurer := &recordingPluginRuntimeCoordinatorPostgresEnsurer{
		delegate: fixture.store,
		first:    make(chan error, 1),
	}
	identity := pluginRuntimeCoordinatorBootstrapTestIdentity(
		"postgres-genesis-wait", extensions.PluginRuntimeProcessAPI,
	)
	var buildCalls atomic.Int32
	var runnerCalls atomic.Int32
	launchCtx, cancelLaunch := context.WithTimeout(fixture.ctx, 5*time.Second)
	defer cancelLaunch()
	result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
	go func() {
		runtime, err := launchPluginRuntimeCoordinator(launchCtx, pluginRuntimeCoordinatorLaunchConfig{
			Identity: identity,
			Ensurer:  ensurer,
			Build: func(
				got extensions.PluginRuntimeNodeIdentity,
				onReady func(),
				_ func(error),
			) (pluginRuntimeCoordinatorRunner, error) {
				buildCalls.Add(1)
				if got != identity {
					return nil, fmt.Errorf("coordinator identity = %#v", got)
				}
				return pluginRuntimeCoordinatorBootstrapTestRunner(func(ctx context.Context) error {
					runnerCalls.Add(1)
					onReady()
					<-ctx.Done()
					return nil
				}), nil
			},
			GenesisWaitTimeout:   3 * time.Second,
			GenesisRetryInterval: 5 * time.Millisecond,
			StopTimeout:          time.Second,
		})
		result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
	}()

	firstErr := waitPluginRuntimeCoordinatorPostgresAttempt(t, ensurer.first)
	if !errors.Is(firstErr, extensions.ErrLifecycleOperationInProgress) {
		t.Fatalf("first genesis error=%v", firstErr)
	}
	fixture.assertOpenLifecycleAndPublicationCount(t, operation.ID, true, 0)
	if buildCalls.Load() != 0 || runnerCalls.Load() != 0 {
		t.Fatalf("builder=%d runner=%d before lifecycle completion", buildCalls.Load(), runnerCalls.Load())
	}
	select {
	case early := <-result:
		t.Fatalf("coordinator returned before lifecycle completion: %#v", early)
	default:
	}

	fixture.completeLifecycle(t, operation)
	started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
	if started.err != nil || started.runtime == nil || !started.runtime.Active() {
		t.Fatalf("runtime=%#v error=%v", started.runtime, started.err)
	}
	if ensurer.calls.Load() < 2 || buildCalls.Load() != 1 || runnerCalls.Load() != 1 {
		t.Fatalf(
			"ensure=%d builder=%d runner=%d",
			ensurer.calls.Load(), buildCalls.Load(), runnerCalls.Load(),
		)
	}
	fixture.assertOpenLifecycleAndPublicationCount(t, operation.ID, false, 1)
	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := started.runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func TestPluginRuntimeCoordinatorPostgresGenesisAbortReleasesProducerSession(t *testing.T) {
	tests := []struct {
		name           string
		waitTimeout    time.Duration
		retryInterval  time.Duration
		cancelAfterTry bool
		want           error
	}{
		{
			name: "context cancellation", waitTimeout: time.Hour,
			retryInterval: time.Hour, cancelAfterTry: true, want: context.Canceled,
		},
		{
			name: "bounded timeout", waitTimeout: time.Nanosecond,
			retryInterval: time.Hour, want: extensions.ErrLifecycleOperationInProgress,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newPluginRuntimeCoordinatorPostgresFixture(t, strings.ReplaceAll(test.name, " ", "_"))
			operation := fixture.acquireOpenLifecycle(t)
			ensurer := &recordingPluginRuntimeCoordinatorPostgresEnsurer{
				delegate: fixture.store,
				first:    make(chan error, 1),
			}
			var buildCalls atomic.Int32
			launchCtx, cancelLaunch := context.WithCancel(fixture.ctx)
			defer cancelLaunch()
			result := make(chan pluginRuntimeCoordinatorBootstrapStartResult, 1)
			go func() {
				runtime, err := launchPluginRuntimeCoordinator(launchCtx, pluginRuntimeCoordinatorLaunchConfig{
					Identity: pluginRuntimeCoordinatorBootstrapTestIdentity(
						"postgres-genesis-abort", extensions.PluginRuntimeProcessWorker,
					),
					Ensurer: ensurer,
					Build: func(
						extensions.PluginRuntimeNodeIdentity, func(), func(error),
					) (pluginRuntimeCoordinatorRunner, error) {
						buildCalls.Add(1)
						return pluginRuntimeCoordinatorBootstrapTestRunner(func(context.Context) error {
							return nil
						}), nil
					},
					GenesisWaitTimeout:   test.waitTimeout,
					GenesisRetryInterval: test.retryInterval,
				})
				result <- pluginRuntimeCoordinatorBootstrapStartResult{runtime: runtime, err: err}
			}()

			firstErr := waitPluginRuntimeCoordinatorPostgresAttempt(t, ensurer.first)
			if !errors.Is(firstErr, extensions.ErrLifecycleOperationInProgress) {
				t.Fatalf("first genesis error=%v", firstErr)
			}
			if test.cancelAfterTry {
				cancelLaunch()
			}
			started := waitPluginRuntimeCoordinatorBootstrapStart(t, result)
			if !errors.Is(started.err, test.want) || started.runtime != nil {
				t.Fatalf("runtime=%#v error=%v want=%v", started.runtime, started.err, test.want)
			}
			if ensurer.calls.Load() != 1 || buildCalls.Load() != 0 {
				t.Fatalf("ensure=%d builder=%d", ensurer.calls.Load(), buildCalls.Load())
			}
			fixture.assertOpenLifecycleAndPublicationCount(t, operation.ID, true, 0)

			fixture.completeLifecycle(t, operation)
			ensureCtx, cancelEnsure := context.WithTimeout(fixture.ctx, 2*time.Second)
			defer cancelEnsure()
			publication, err := fixture.store.EnsureInitialPluginRuntimePublication(ensureCtx)
			if err != nil || publication.Revision != 1 {
				t.Fatalf("post-abort genesis=%+v error=%v", publication, err)
			}
			fixture.assertOpenLifecycleAndPublicationCount(t, operation.ID, false, 1)
		})
	}
}

func newPluginRuntimeCoordinatorPostgresFixture(
	t *testing.T,
	label string,
) *pluginRuntimeCoordinatorPostgresFixture {
	t.Helper()
	databaseURL := testdb.IsolatedURL(t)
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("prc_%d_%s", time.Now().UnixNano(), label)
	quotedSchema := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	var pool *pgxpool.Pool
	t.Cleanup(func() {
		if pool != nil {
			pool.Close()
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanupCtx, "DROP SCHEMA IF EXISTS "+quotedSchema+" CASCADE"); err != nil {
			t.Errorf("drop private coordinator schema: %v", err)
			admin.Close()
			return
		}
		var schemaExists bool
		if err := admin.QueryRow(cleanupCtx, `
			SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)
		`, schema).Scan(&schemaExists); err != nil {
			t.Errorf("inspect private coordinator schema cleanup: %v", err)
		} else if schemaExists {
			t.Errorf("private coordinator schema %q was not removed", schema)
		}
		admin.Close()
	})

	migrationConfig, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	migrationConfig.RuntimeParams["search_path"] = schema + ",public"
	migrationDB := stdlib.OpenDB(*migrationConfig)
	migrationDB.SetMaxOpenConns(1)
	migrationDB.SetMaxIdleConns(1)
	defer func() {
		if migrationDB != nil {
			_ = migrationDB.Close()
		}
	}()
	// 只建立三个生产迁移的最小前置表；lifecycle 与 genesis 结构仍由
	// embedded Goose migration 创建，避免测试复制生产账本定义。
	if _, err := migrationDB.ExecContext(ctx, `
		CREATE TABLE users (id BIGINT PRIMARY KEY);
		CREATE TABLE extension_trust_grants (id BIGSERIAL PRIMARY KEY);
		CREATE TABLE extensions (
			id TEXT PRIMARY KEY,
			type TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'installed',
			active_version_id BIGINT
		);
		CREATE TABLE extension_versions (
			id BIGINT PRIMARY KEY,
			extension_id TEXT NOT NULL REFERENCES extensions(id) ON DELETE CASCADE,
			version TEXT NOT NULL,
			package_digest TEXT NOT NULL,
			manifest JSONB NOT NULL DEFAULT '{}'::jsonb
		);
		ALTER TABLE extensions
			ADD CONSTRAINT extensions_active_version_fk
			FOREIGN KEY (active_version_id) REFERENCES extension_versions(id) ON DELETE SET NULL;
	`); err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(
		goose.DialectPostgres,
		migrationDB,
		migrations.Files(),
		goose.WithDisableGlobalRegistry(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range []int64{
		pluginRuntimeCoordinatorLifecycleMigration,
		pluginRuntimeCoordinatorRecoveryMigration,
		pluginRuntimeCoordinatorGenesisMigration,
	} {
		if _, err := provider.ApplyVersion(ctx, version, true); err != nil {
			t.Fatalf("apply migration %d: %v", version, err)
		}
	}
	if err := migrationDB.Close(); err != nil {
		t.Fatal(err)
	}
	migrationDB = nil

	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	poolConfig.ConnConfig.RuntimeParams["application_name"] = schema
	poolConfig.MaxConns = 4
	pool, err = pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	var currentSchema string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&currentSchema); err != nil {
		t.Fatal(err)
	}
	if currentSchema != schema {
		t.Fatalf("current schema=%q want=%q", currentSchema, schema)
	}
	extensionID := "bootstrap.runtime.genesis"
	if _, err := pool.Exec(ctx, `
		INSERT INTO extensions (id, type, status) VALUES ($1, 'plugin', 'installed')
	`, extensionID); err != nil {
		t.Fatal(err)
	}
	store := extensions.NewPostgresStore(pool)
	return &pluginRuntimeCoordinatorPostgresFixture{
		ctx: ctx, pool: pool, store: store,
		repository: extensions.NewPostgresLifecycleRepository(pool),
		schema:     schema, extensionID: extensionID,
	}
}

func (fixture *pluginRuntimeCoordinatorPostgresFixture) acquireOpenLifecycle(
	t *testing.T,
) extensions.LifecycleOperation {
	t.Helper()
	result, err := fixture.repository.AcquireOperation(fixture.ctx, extensions.AcquireLifecycleOperationInput{
		ExtensionID: fixture.extensionID, ExtensionVersion: "1.0.0",
		PackageDigest: strings.Repeat("a", 64), ArtifactDigests: json.RawMessage(`{}`),
		Operation: extensions.LifecycleOperationEnable, PlanVersion: "bootstrap.genesis@1",
		IdempotencyKey: "enable:first", RequestFingerprint: strings.Repeat("b", 64),
		AuthorityType:     extensions.LifecycleAuthorityBuiltin,
		AuthoritySnapshot: json.RawMessage(`{"source":"bootstrap-postgres-test"}`),
	})
	if err != nil || !result.Created || result.Operation.CompletedAt != nil {
		t.Fatalf("acquire lifecycle=%+v error=%v", result, err)
	}
	return result.Operation
}

func (fixture *pluginRuntimeCoordinatorPostgresFixture) completeLifecycle(
	t *testing.T,
	operation extensions.LifecycleOperation,
) {
	t.Helper()
	completed, err := fixture.repository.CompleteOperation(
		fixture.ctx,
		extensions.CompleteLifecycleOperationInput{
			OperationID: operation.ID, ExpectedRevision: operation.Revision,
			ExpectedState: operation.State, State: extensions.LifecycleStateEnabled,
			TerminalResult: extensions.LifecycleTerminalSucceeded,
			ResultDocument: json.RawMessage(`{"enabled":true}`),
		},
	)
	if err != nil || completed.CompletedAt == nil {
		t.Fatalf("complete lifecycle=%+v error=%v", completed, err)
	}
}

func (fixture *pluginRuntimeCoordinatorPostgresFixture) assertOpenLifecycleAndPublicationCount(
	t *testing.T,
	operationID int64,
	wantOpen bool,
	wantPublications int,
) {
	t.Helper()
	var open bool
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT completed_at IS NULL FROM extension_lifecycle_operations WHERE id = $1
	`, operationID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	var publications int
	if err := fixture.pool.QueryRow(fixture.ctx, `
		SELECT count(*) FROM plugin_runtime_publications
	`).Scan(&publications); err != nil {
		t.Fatal(err)
	}
	if open != wantOpen || publications != wantPublications {
		t.Fatalf(
			"open lifecycle=%t want=%t publications=%d want=%d",
			open, wantOpen, publications, wantPublications,
		)
	}
}

func waitPluginRuntimeCoordinatorPostgresAttempt(t *testing.T, attempts <-chan error) error {
	t.Helper()
	select {
	case err := <-attempts:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("plugin runtime coordinator genesis attempt did not return")
		return nil
	}
}
