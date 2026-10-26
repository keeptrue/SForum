package extensions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRetryablePluginRuntimePublicationError(t *testing.T) {
	for _, code := range []string{"40001", "40P01"} {
		if !retryablePluginRuntimePublicationError(fmt.Errorf("transaction: %w", &pgconn.PgError{Code: code})) {
			t.Fatalf("PostgreSQL %s must be retried", code)
		}
	}
	if retryablePluginRuntimePublicationError(&pgconn.PgError{Code: "23505"}) ||
		retryablePluginRuntimePublicationError(errors.New("not a PostgreSQL transaction error")) {
		t.Fatal("non-serialization errors must not be retried")
	}
}

func TestEnsureInitialPluginRuntimePublicationExactlyOnceAcrossRestart(t *testing.T) {
	for _, test := range []struct {
		name    string
		members []PluginRuntimeMember
		setup   func(*testing.T, *pluginRuntimePublicationPGFixture)
	}{
		{name: "empty"},
		{
			name: "non-empty",
			members: []PluginRuntimeMember{{
				ExtensionID: "fixture.plugin", ExtensionVersionID: 101,
				ExtensionVersion: "1.0.0", PackageDigest: strings.Repeat("b", 64),
			}},
			setup: func(t *testing.T, fixture *pluginRuntimePublicationPGFixture) {
				setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
					runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPluginRuntimePublicationPGFixture(
				t, "initial_once_"+strings.ReplaceAll(test.name, "-", "_"),
			)
			if test.setup != nil {
				test.setup(t, fixture)
			}

			initial, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if initial.Revision != 1 || initial.Reason != PluginRuntimePublicationStartupReconcile ||
				initial.ActorUserID != 0 {
				t.Fatalf("unexpected initial publication: %+v", initial)
			}
			assertPluginRuntimePublicationMembers(t, initial, test.members...)
			var actorIsNull bool
			if err := fixture.pool.QueryRow(fixture.ctx, `
				SELECT actor_user_id IS NULL
				FROM plugin_runtime_publications
				WHERE revision = $1
			`, initial.Revision).Scan(&actorIsNull); err != nil || !actorIsNull {
				t.Fatalf("genesis actor null=%t error=%v", actorIsNull, err)
			}

			// ACCESS EXCLUSIVE makes any accidental legacy extensions scan block.
			// Existing immutable history must return without touching those rows.
			assertInitialPluginRuntimeReplayWithoutMutableRead(t, fixture, initial)
			restartedPool, err := pgxpool.NewWithConfig(fixture.ctx, fixture.pool.Config().Copy())
			if err != nil {
				t.Fatal(err)
			}
			defer restartedPool.Close()
			restarted := NewPostgresStore(restartedPool)
			replayed, err := restarted.EnsureInitialPluginRuntimePublication(fixture.ctx)
			if err != nil || !samePluginRuntimePublication(replayed, initial) {
				t.Fatalf("restart replay=%+v error=%v", replayed, err)
			}
			assertPluginRuntimePublicationCount(t, fixture, 1)
		})
	}
}

func TestEnsureInitialPluginRuntimePublicationImportsOnlyExecutablePlugins(t *testing.T) {
	fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_exclusions")
	setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
		runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))
	setPluginRuntimeFixtureVersion(t, fixture, "second.plugin", 102, StatusEnabled,
		runtimeManifestBody(t, "second.plugin", "2.0.0", TypePlugin, ""))
	setPluginRuntimeFixtureVersion(t, fixture, "fixture.theme", 103, StatusEnabled,
		runtimeManifestBody(t, "fixture.theme", "1.0.0", TypeTheme, ""))

	publication, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertPluginRuntimePublicationMembers(t, publication, fixture.firstMember())
	if publication.Reason != PluginRuntimePublicationStartupReconcile || publication.ActorUserID != 0 {
		t.Fatalf("unexpected genesis evidence: %+v", publication)
	}
}

func TestEnsureInitialPluginRuntimePublicationRejectsInvalidLegacyProjection(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *pluginRuntimePublicationPGFixture)
	}{
		{
			name: "missing active version",
			setup: func(t *testing.T, fixture *pluginRuntimePublicationPGFixture) {
				_, err := fixture.pool.Exec(fixture.ctx, `
					UPDATE extensions SET status = 'enabled', active_version_id = NULL
					WHERE id = 'fixture.plugin'
				`)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "corrupt manifest shape",
			setup: func(t *testing.T, fixture *pluginRuntimePublicationPGFixture) {
				setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled, []byte(`{"id":42}`))
			},
		},
		{
			name: "manifest identity mismatch",
			setup: func(t *testing.T, fixture *pluginRuntimePublicationPGFixture) {
				setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
					runtimeManifestBody(t, "other.plugin", "1.0.0", TypePlugin, "backend/plugin"))
			},
		},
		{
			name: "active version belongs to another extension",
			setup: func(t *testing.T, fixture *pluginRuntimePublicationPGFixture) {
				setPluginRuntimeFixtureVersion(t, fixture, "second.plugin", 102, StatusInstalled,
					runtimeManifestBody(t, "second.plugin", "2.0.0", TypePlugin, "backend/plugin"))
				_, err := fixture.pool.Exec(fixture.ctx, `
					UPDATE extensions SET status = 'enabled', active_version_id = 102
					WHERE id = 'fixture.plugin'
				`)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPluginRuntimePublicationPGFixture(t, fmt.Sprintf("initial_invalid_%d", index))
			test.setup(t, fixture)
			_, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
			if !errors.Is(err, ErrPluginRuntimePublicationConflict) {
				t.Fatalf("expected fail-closed conflict, got %v", err)
			}
			assertPluginRuntimePublicationCount(t, fixture, 0)
		})
	}
}

func TestEnsureInitialPluginRuntimePublicationBlocksForOpenLifecycle(t *testing.T) {
	fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_open_lifecycle")
	if _, err := fixture.pool.Exec(fixture.ctx, `
		INSERT INTO extension_lifecycle_operations (completed_at) VALUES (NULL)
	`); err != nil {
		t.Fatal(err)
	}

	mutableLock, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutableLock.Exec(fixture.ctx, `LOCK TABLE extensions IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = mutableLock.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	ensureCtx, cancelEnsure := context.WithTimeout(fixture.ctx, 2*time.Second)
	defer cancelEnsure()
	_, ensureErr := fixture.store.EnsureInitialPluginRuntimePublication(ensureCtx)
	if rollbackErr := mutableLock.Rollback(fixture.ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if !errors.Is(ensureErr, ErrLifecycleOperationInProgress) {
		t.Fatalf("open lifecycle error=%v", ensureErr)
	}
	assertPluginRuntimePublicationCount(t, fixture, 0)

	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_lifecycle_operations SET completed_at = statement_timestamp()
	`); err != nil {
		t.Fatal(err)
	}
	publication, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
	if err != nil || publication.Revision != 1 {
		t.Fatalf("retry publication=%+v error=%v", publication, err)
	}
	assertPluginRuntimePublicationMembers(t, publication)
}

func TestEnsureInitialPluginRuntimePublicationConcurrentAPIWorkerGenesis(t *testing.T) {
	fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_concurrent")
	setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
		runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))
	setPluginRuntimeFixtureVersion(t, fixture, "second.plugin", 102, StatusEnabled,
		runtimeManifestBody(t, "second.plugin", "2.0.0", TypePlugin, "backend/plugin"))
	workerPool, err := pgxpool.NewWithConfig(fixture.ctx, fixture.pool.Config().Copy())
	if err != nil {
		t.Fatal(err)
	}
	defer workerPool.Close()
	producers := []*PostgresStore{fixture.store, NewPostgresStore(workerPool)}

	const producerCount = 16
	results := make(chan PluginRuntimePublication, producerCount)
	errorsFound := make(chan error, producerCount)
	publishCtx, cancelPublish := context.WithTimeout(fixture.ctx, 10*time.Second)
	defer cancelPublish()
	var wait sync.WaitGroup
	for index := 0; index < producerCount; index++ {
		wait.Add(1)
		go func(store *PostgresStore) {
			defer wait.Done()
			publication, err := store.EnsureInitialPluginRuntimePublication(publishCtx)
			if err != nil {
				errorsFound <- err
				return
			}
			results <- publication
		}(producers[index%len(producers)])
	}
	done := make(chan struct{})
	go func() {
		wait.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		cancelPublish()
		t.Fatal("concurrent producers did not finish")
	}
	close(results)
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("concurrent producer failed: %v", err)
	}
	for publication := range results {
		if publication.Revision != 1 {
			t.Errorf("concurrent producer created revision %d, want 1", publication.Revision)
		}
		assertPluginRuntimePublicationMembers(t, publication, fixture.firstMember(), fixture.secondMember())
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)
}

func TestEnsureInitialPluginRuntimePublicationDoesNotOverwriteLifecycleTransition(t *testing.T) {
	fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_transition_race")
	target := transitionFixturePlugin(
		t, "fixture.plugin", 101, "1.0.0", strings.Repeat("b", 64), "backend/plugin",
	)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions SET manifest = $1::jsonb WHERE id = 101
	`, string(runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions SET status = 'enabled', active_version_id = NULL WHERE id = 'fixture.plugin'
	`); err != nil {
		t.Fatal(err)
	}

	transitionTx, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = transitionTx.Rollback(context.Background()) }()
	var transitionPID int32
	if err := transitionTx.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&transitionPID); err != nil {
		t.Fatal(err)
	}
	if _, err := transitionTx.Exec(fixture.ctx, `
		SELECT pg_advisory_xact_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		t.Fatal(err)
	}
	if _, err := transitionTx.Exec(fixture.ctx, `
		INSERT INTO extension_lifecycle_operations (completed_at) VALUES (NULL)
	`); err != nil {
		t.Fatal(err)
	}

	type ensureResult struct {
		publication PluginRuntimePublication
		err         error
	}
	result := make(chan ensureResult, 1)
	go func() {
		publication, ensureErr := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
		result <- ensureResult{publication: publication, err: ensureErr}
	}()
	waitForPluginRuntimeAdvisoryLockWaiter(t, fixture, transitionPID)

	transition, err := PublishPluginRuntimePublicationTransitionTx(
		fixture.ctx, transitionTx, PluginRuntimePublicationTransition{
			Target: target, Activate: true,
			Reason: PluginRuntimePublicationEnable, ActorUserID: 99,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transitionTx.Commit(fixture.ctx); err != nil {
		t.Fatal(err)
	}

	select {
	case ensured := <-result:
		if ensured.err != nil || !samePluginRuntimePublication(ensured.publication, transition) {
			t.Fatalf("initial ensure overwrote transition: ensured=%+v transition=%+v error=%v",
				ensured.publication, transition, ensured.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("initial ensure did not resume after lifecycle transition")
	}
	assertPluginRuntimePublicationCount(t, fixture, 1)
}

func TestEnsureInitialPluginRuntimePublicationRollsBackPostgresErrors(t *testing.T) {
	t.Run("lifecycle inspection", func(t *testing.T) {
		fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_lifecycle_pg_error")
		if _, err := fixture.pool.Exec(fixture.ctx, `
			ALTER TABLE extension_lifecycle_operations RENAME TO unavailable_lifecycle_operations;
			CREATE VIEW extension_lifecycle_operations AS SELECT 1::bigint AS id
		`); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
		var postgresErr *pgconn.PgError
		if !errors.As(err, &postgresErr) || postgresErr.Code != "42703" {
			t.Fatalf("lifecycle PostgreSQL error=%v", err)
		}
		assertPluginRuntimePublicationCount(t, fixture, 0)
	})

	t.Run("member insert", func(t *testing.T) {
		fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_insert_rollback")
		setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
			runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))
		if _, err := fixture.pool.Exec(fixture.ctx, `
			CREATE FUNCTION reject_initial_plugin_runtime_member() RETURNS trigger
			LANGUAGE plpgsql AS $$
			BEGIN
				RAISE EXCEPTION 'injected initial member failure' USING ERRCODE = 'XX000';
			END;
			$$;
			CREATE TRIGGER reject_initial_plugin_runtime_member
			BEFORE INSERT ON plugin_runtime_publication_members
			FOR EACH ROW EXECUTE FUNCTION reject_initial_plugin_runtime_member()
		`); err != nil {
			t.Fatal(err)
		}
		_, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
		var postgresErr *pgconn.PgError
		if !errors.As(err, &postgresErr) || postgresErr.Code != "XX000" {
			t.Fatalf("member PostgreSQL error=%v", err)
		}
		assertPluginRuntimePublicationCount(t, fixture, 0)

		if _, err := fixture.pool.Exec(fixture.ctx, `
			DROP TRIGGER reject_initial_plugin_runtime_member ON plugin_runtime_publication_members;
			DROP FUNCTION reject_initial_plugin_runtime_member()
		`); err != nil {
			t.Fatal(err)
		}
		publication, err := fixture.store.EnsureInitialPluginRuntimePublication(fixture.ctx)
		if err != nil || publication.Revision != 2 {
			// PostgreSQL sequences are non-transactional; the rolled-back attempt consumed revision 1.
			t.Fatalf("retry publication=%+v error=%v", publication, err)
		}
		assertPluginRuntimePublicationMembers(t, publication, fixture.firstMember())
		assertPluginRuntimePublicationCount(t, fixture, 1)
	})
}

func TestLoadLegacyInitialPluginRuntimeMembersLocksCompleteProjection(t *testing.T) {
	tests := []struct {
		name   string
		update string
	}{
		{
			name:   "extension state",
			update: `UPDATE extensions SET status = 'disabled' WHERE id = 'fixture.plugin'`,
		},
		{
			name: "version manifest",
			update: `UPDATE extension_versions
				SET manifest = manifest || '{"description":"changed"}'::jsonb
				WHERE id = 101`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_projection_lock")
			setPluginRuntimeFixtureVersion(t, fixture, "fixture.plugin", 101, StatusEnabled,
				runtimeManifestBody(t, "fixture.plugin", "1.0.0", TypePlugin, "backend/plugin"))

			tx, err := fixture.pool.BeginTx(fixture.ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
			if err != nil {
				t.Fatal(err)
			}
			members, err := loadLegacyInitialPluginRuntimeMembers(fixture.ctx, tx)
			if err != nil || len(members) != 1 || members[0] != fixture.firstMember() {
				_ = tx.Rollback(fixture.ctx)
				t.Fatalf("members=%+v error=%v", members, err)
			}

			updater, err := fixture.pool.Acquire(fixture.ctx)
			if err != nil {
				_ = tx.Rollback(fixture.ctx)
				t.Fatal(err)
			}
			defer updater.Release()
			var updaterPID int32
			if err := updater.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&updaterPID); err != nil {
				_ = tx.Rollback(fixture.ctx)
				t.Fatal(err)
			}

			updateCtx, cancelUpdate := context.WithTimeout(fixture.ctx, 5*time.Second)
			defer cancelUpdate()
			updated := make(chan error, 1)
			go func() {
				_, updateErr := updater.Exec(updateCtx, test.update)
				updated <- updateErr
			}()

			blocked := false
			deadline := time.Now().Add(2 * time.Second)
			for time.Now().Before(deadline) {
				select {
				case updateErr := <-updated:
					_ = tx.Rollback(fixture.ctx)
					t.Fatalf("projection mutation completed before transaction release: %v", updateErr)
				default:
				}
				var blockerCount int
				if err := fixture.pool.QueryRow(fixture.ctx, `
					SELECT cardinality(pg_blocking_pids($1))
				`, updaterPID).Scan(&blockerCount); err != nil {
					_ = tx.Rollback(fixture.ctx)
					t.Fatal(err)
				}
				if blockerCount > 0 {
					blocked = true
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if !blocked {
				_ = tx.Rollback(fixture.ctx)
				<-updated
				t.Fatal("projection mutation never blocked on the producer transaction")
			}
			if err := tx.Commit(fixture.ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-updated; err != nil {
				t.Fatalf("projection mutation did not resume after commit: %v", err)
			}
		})
	}
}

func TestReleasePluginRuntimeProducerConnectionDropsUnownedSession(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "authoritative_unlock_false")
	connection, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	// No advisory lock was acquired on this session, so PostgreSQL returns
	// false. The release path must close it instead of poisoning the pool.
	destroyed, err := releasePluginRuntimeProducerConnection(fixture.ctx, connection, true)
	if err != nil || !destroyed {
		t.Fatalf("unlock false destroyed=%t error=%v", destroyed, err)
	}
	if acquired := fixture.pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("hijacked connection remains acquired: %d", acquired)
	}
	if err := fixture.pool.Ping(fixture.ctx); err != nil {
		t.Fatalf("pool did not replace closed producer connection: %v", err)
	}
}

func TestFinalizePluginRuntimeProducerResultRecoversWithSingleConnection(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "authoritative_single_connection")
	expected, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name        string
		acquireLock bool
	}{
		{name: "released session", acquireLock: true},
		{name: "destroyed session", acquireLock: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fixture.pool.Config().Copy()
			config.MinConns = 0
			config.MaxConns = 1
			pool, err := pgxpool.NewWithConfig(fixture.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			store := NewPostgresStore(pool)
			connection, err := pool.Acquire(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if test.acquireLock {
				if _, err := connection.Exec(fixture.ctx, `
					SELECT pg_advisory_lock(hashtextextended($1, 0))
				`, pluginRuntimeDesiredSetLock); err != nil {
					connection.Release()
					t.Fatal(err)
				}
			}

			recovered, err := store.finalizePluginRuntimeProducerResult(
				fixture.ctx,
				connection,
				true,
				expected,
				&pluginRuntimePublicationCommitUnknown{cause: errors.New("ambiguous commit transport")},
			)
			if err != nil || !samePluginRuntimePublication(recovered, expected) {
				t.Fatalf("recovered=%+v error=%v", recovered, err)
			}
			if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
				t.Fatalf("producer connection remained acquired: %d", acquired)
			}
		})
	}
}

type pluginRuntimeCloseErrorConn struct {
	net.Conn
	err              error
	underlyingClosed *atomic.Bool
}

func (c *pluginRuntimeCloseErrorConn) Close() error {
	underlyingErr := c.Conn.Close()
	c.underlyingClosed.Store(true)
	return errors.Join(underlyingErr, c.err)
}

func TestFinalizePluginRuntimeProducerResultRecoversAfterDestroyedCloseError(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "authoritative_close_error")
	stored, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name     string
		expected PluginRuntimePublication
		found    bool
	}{
		{name: "exact revision committed", expected: stored, found: true},
		{name: "exact revision absent", expected: func() PluginRuntimePublication {
			missing := stored
			missing.Revision++
			return missing
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := fixture.pool.Config().Copy()
			config.MinConns = 0
			config.MaxConns = 1
			originalDial := config.ConnConfig.DialFunc
			closeErr := errors.New("injected producer connection close error")
			var dialCount atomic.Int32
			var underlyingClosed atomic.Bool
			config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				connection, dialErr := originalDial(ctx, network, address)
				if dialErr == nil && dialCount.Add(1) == 1 {
					return &pluginRuntimeCloseErrorConn{
						Conn: connection, err: closeErr, underlyingClosed: &underlyingClosed,
					}, nil
				}
				return connection, dialErr
			}
			pool, err := pgxpool.NewWithConfig(fixture.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()

			producer, err := pool.Acquire(fixture.ctx)
			if err != nil {
				t.Fatal(err)
			}
			commitErr := errors.New("ambiguous commit transport")
			recovered, err := NewPostgresStore(pool).finalizePluginRuntimeProducerResult(
				fixture.ctx,
				producer,
				true,
				test.expected,
				&pluginRuntimePublicationCommitUnknown{cause: commitErr},
			)
			if test.found {
				if err != nil || !samePluginRuntimePublication(recovered, stored) {
					t.Fatalf("recovered=%+v error=%v", recovered, err)
				}
			} else if err == nil || !errors.Is(err, commitErr) ||
				!errors.Is(err, closeErr) || !errors.Is(err, ErrPluginRuntimePublicationNotFound) {
				t.Fatalf("missing recovery error=%v", err)
			}
			if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
				t.Fatalf("producer connection remained acquired: %d", acquired)
			}
			if !underlyingClosed.Load() {
				t.Fatal("producer wrapper returned its close sentinel before closing the underlying connection")
			}
			if got := dialCount.Load(); got < 2 {
				t.Fatalf("exact recovery did not use a replacement physical connection: dials=%d", got)
			}
		})
	}
}

func TestEnsureInitialPluginRuntimePublicationCancelWhileWaitingDestroysSession(t *testing.T) {
	fixture := newInitialPluginRuntimePublicationPGFixture(t, "initial_cancel_lock")
	blocker, err := fixture.pool.Acquire(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	locked := false
	defer func() {
		if locked {
			_, _ = blocker.Exec(context.Background(), `
				SELECT pg_advisory_unlock(hashtextextended($1, 0))
			`, pluginRuntimeDesiredSetLock)
		}
		blocker.Release()
	}()
	var blockerPID int32
	if err := blocker.QueryRow(fixture.ctx, `SELECT pg_backend_pid()`).Scan(&blockerPID); err != nil {
		t.Fatal(err)
	}
	if _, err := blocker.Exec(fixture.ctx, `
		SELECT pg_advisory_lock(hashtextextended($1, 0))
	`, pluginRuntimeDesiredSetLock); err != nil {
		t.Fatal(err)
	}
	locked = true

	publishCtx, cancelPublish := context.WithCancel(fixture.ctx)
	result := make(chan error, 1)
	go func() {
		_, publishErr := fixture.store.EnsureInitialPluginRuntimePublication(publishCtx)
		result <- publishErr
	}()

	var waitingPID int32
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		err := fixture.pool.QueryRow(fixture.ctx, `
			SELECT waiter.pid
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
			LIMIT 1
		`, blockerPID, fixture.schema).Scan(&waitingPID)
		if err == nil {
			break
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			cancelPublish()
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waitingPID == 0 {
		cancelPublish()
		<-result
		t.Fatal("producer never blocked on the advisory lock")
	}

	cancelPublish()
	select {
	case publishErr := <-result:
		if !errors.Is(publishErr, context.Canceled) {
			t.Fatalf("cancelled producer error=%v", publishErr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled producer did not return")
	}

	backendClosed := false
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var exists bool
		if err := fixture.pool.QueryRow(fixture.ctx, `
			SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1)
		`, waitingPID).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if !exists {
			backendClosed = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !backendClosed {
		t.Fatalf("cancelled producer backend %d remained alive", waitingPID)
	}
	if acquired := fixture.pool.Stat().AcquiredConns(); acquired != 1 {
		t.Fatalf("pool retained cancelled producer connection: acquired=%d", acquired)
	}
}

func TestRecoverAuthoritativePluginRuntimePublicationUsesExactCommittedRevision(t *testing.T) {
	fixture := newPluginRuntimePublicationPGFixture(t, "authoritative_commit_recovery")
	expected, err := fixture.store.publishPluginRuntimePublication(
		fixture.ctx, PluginRuntimePublicationStartupReconcile, 0, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := fixture.store.recoverAuthoritativePluginRuntimePublication(
		fixture.ctx, expected, errors.New("ambiguous commit transport"),
	)
	if err != nil || !samePluginRuntimePublication(recovered, expected) {
		t.Fatalf("recovered=%+v error=%v", recovered, err)
	}

	missing := expected
	missing.Revision++
	if _, err := fixture.store.recoverAuthoritativePluginRuntimePublication(
		fixture.ctx, missing, errors.New("commit did not happen"),
	); err == nil || !strings.Contains(err.Error(), "commit did not happen") {
		t.Fatalf("missing commit recovery error=%v", err)
	}
}

func runtimeManifestBody(t *testing.T, id, version, extensionType, backendEntry string) []byte {
	t.Helper()
	manifest := Manifest{
		ManifestVersion: 3,
		ID:              id, Name: "Runtime Fixture", Description: "Runtime publication fixture.",
		URL: "https://example.com/runtime-fixture", Author: ManifestAuthor{Name: "SForum"},
		Version: version, Type: extensionType, SForumVersion: ">=0.1.0",
	}
	if backendEntry != "" {
		backendDigest := strings.Repeat("e", 64)
		manifest.Backend = ManifestBackend{
			Entry: backendEntry, RPC: "hashicorp-go-plugin", ProtocolVersion: 2,
			Digest: backendDigest, HostAPIVersion: "sforum.host@2",
		}
		manifest.PackageFiles = []ManifestPackageFile{{
			ID: id + ".file.backend", Kind: "executable", Path: backendEntry, Digest: backendDigest,
		}}
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func setPluginRuntimeFixtureVersion(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	extensionID string,
	versionID int64,
	status string,
	manifest []byte,
) {
	t.Helper()
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extension_versions SET manifest = $3::jsonb
		WHERE id = $1 AND extension_id = $2
	`, versionID, extensionID, string(manifest)); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.pool.Exec(fixture.ctx, `
		UPDATE extensions SET status = $3, active_version_id = $1
		WHERE id = $2
	`, versionID, extensionID, status); err != nil {
		t.Fatal(err)
	}
}

func newInitialPluginRuntimePublicationPGFixture(
	t *testing.T,
	label string,
) *pluginRuntimePublicationPGFixture {
	t.Helper()
	fixture := newPluginRuntimePublicationPGFixture(t, label)
	if _, err := fixture.pool.Exec(fixture.ctx, `
		CREATE TABLE extension_lifecycle_operations (
			id BIGSERIAL PRIMARY KEY,
			completed_at TIMESTAMPTZ
		)
	`); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func assertInitialPluginRuntimeReplayWithoutMutableRead(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	expected PluginRuntimePublication,
) {
	t.Helper()
	mutableLock, err := fixture.pool.Begin(fixture.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mutableLock.Exec(fixture.ctx, `LOCK TABLE extensions IN ACCESS EXCLUSIVE MODE`); err != nil {
		_ = mutableLock.Rollback(fixture.ctx)
		t.Fatal(err)
	}
	replayCtx, cancelReplay := context.WithTimeout(fixture.ctx, 2*time.Second)
	defer cancelReplay()
	replayed, replayErr := fixture.store.EnsureInitialPluginRuntimePublication(replayCtx)
	if rollbackErr := mutableLock.Rollback(fixture.ctx); rollbackErr != nil {
		t.Fatal(rollbackErr)
	}
	if replayErr != nil || !samePluginRuntimePublication(replayed, expected) {
		t.Fatalf("existing revision read mutable state: replay=%+v error=%v", replayed, replayErr)
	}
}

func waitForPluginRuntimeAdvisoryLockWaiter(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	blockerPID int32,
) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var waitingPID int32
		err := fixture.pool.QueryRow(fixture.ctx, `
			SELECT waiter.pid
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
			LIMIT 1
		`, blockerPID, fixture.schema).Scan(&waitingPID)
		if err == nil && waitingPID != 0 {
			return
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("initial producer never blocked on the lifecycle advisory lock")
}

func assertPluginRuntimePublicationMembers(
	t *testing.T,
	publication PluginRuntimePublication,
	members ...PluginRuntimeMember,
) {
	t.Helper()
	canonical, digest, err := canonicalPluginRuntimeMembers(members)
	if err != nil {
		t.Fatal(err)
	}
	if publication.MemberCount != len(canonical) || publication.MembersDigest != digest ||
		len(publication.Members) != len(canonical) {
		t.Fatalf("publication set mismatch: publication=%+v members=%+v", publication, canonical)
	}
	for index := range canonical {
		if publication.Members[index] != canonical[index] {
			t.Fatalf("publication member[%d]=%+v, want %+v", index, publication.Members[index], canonical[index])
		}
	}
}

func assertPluginRuntimePublicationCount(
	t *testing.T,
	fixture *pluginRuntimePublicationPGFixture,
	want int,
) {
	t.Helper()
	var count int
	if err := fixture.pool.QueryRow(fixture.ctx, `SELECT count(*) FROM plugin_runtime_publications`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("publication count=%d, want %d", count, want)
	}
}
