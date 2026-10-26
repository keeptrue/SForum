package bootstrap

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"

	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	"github.com/keeptrue/sforum/apps/api/config"
	"github.com/keeptrue/sforum/apps/api/database/migrator"
)

func TestProductionQueryWorkerOwnershipAndSafeModeKindJoined(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("SFORUM_TEST_DATABASE_URL"))
	redisAddress := strings.TrimSpace(os.Getenv("SFORUM_QUERY_CACHE_TEST_REDIS_ADDR"))
	redisPassword := os.Getenv("SFORUM_QUERY_CACHE_TEST_REDIS_PASSWORD")
	expectedRedisRunID := strings.TrimSpace(os.Getenv("SFORUM_QUERY_CACHE_JOINED_EXPECTED_REDIS_RUN_ID"))
	ownershipToken := strings.TrimSpace(os.Getenv("SFORUM_QUERY_CACHE_JOINED_OWNERSHIP_TOKEN"))
	seed := strings.TrimSpace(os.Getenv("SFORUM_QUERY_CACHE_JOINED_SEED"))
	if baseURL == "" || redisAddress == "" || expectedRedisRunID == "" || ownershipToken == "" || seed == "" {
		t.Skip("set the joined PostgreSQL and runner-owned Redis environment")
	}
	if !strings.HasSuffix(seed, "-"+ownershipToken) {
		t.Fatal("joined Query seed is not bound to the runner ownership token")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	installationDigest := sha256.Sum256([]byte("p7-query-worker-ownership\x00" + ownershipToken))
	installationID := hex.EncodeToString(installationDigest[:])
	apiCache, err := loadOptionalProductionQueryResultCache(
		ctx,
		p7QueryRedisOwnershipConfig(redisAddress, redisPassword, false),
		installationID,
		logger,
	)
	if err != nil {
		t.Fatalf("load production API Query cache authority: %v", err)
	}
	if apiCache == nil || apiCache.cache == nil || apiCache.client == nil {
		t.Fatal("production API Query cache failed open against the runner-owned Redis authority")
	}
	t.Cleanup(func() { apiCache.Close(logger) })
	if got := p7QueryRedisRunID(t, ctx, apiCache.client); got != expectedRedisRunID {
		t.Fatalf("production API Query cache endpoint run_id=%q, want %q", got, expectedRedisRunID)
	}
	databaseURL := p7QueryWorkerOwnershipDatabaseURL(t, baseURL, seed)
	if err := migrator.Up(ctx, migrator.Config{DatabaseURL: databaseURL, Logger: logger}); err != nil {
		t.Fatalf("migrate Query worker ownership database: %v", err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Query worker ownership fixture path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", "..", ".."))
	extensionRoot := t.TempDir()
	builtinRoot := filepath.Join(repositoryRoot, "extensions", "builtin")
	if _, err := extensions.NewServiceWithBuiltins(
		extensions.NewPostgresStore(pool), extensionRoot, builtinRoot,
	).SyncBuiltins(ctx); err != nil {
		t.Fatalf("synchronize Query worker ownership builtins: %v", err)
	}

	originalManager := newStandaloneWorkerRuntimeManager
	var standaloneRuntimes []*countingWorkerRuntime
	newStandaloneWorkerRuntimeManager = func(
		extensions.Store,
		extensionsruntime.HostAPIRegistrar,
		extensionsruntime.PluginSettings,
		extensionsruntime.RuntimeTrustSource,
		extensionsruntime.RuntimeDatabaseLeaseRegistry,
	) workerExtensionRuntime {
		runtime := &countingWorkerRuntime{}
		standaloneRuntimes = append(standaloneRuntimes, runtime)
		return runtime
	}
	t.Cleanup(func() { newStandaloneWorkerRuntimeManager = originalManager })
	stubStandaloneWorkerRuntimeCoordinator(t, nil)

	var normalAuthorities []*productionQueryResultCacheRuntime
	tests := []struct {
		name     string
		embedded bool
		safeMode bool
	}{
		{name: "embedded normal", embedded: true},
		{name: "standalone normal"},
		{name: "embedded safe mode", embedded: true, safeMode: true},
		{name: "standalone safe mode", safeMode: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := p7QueryWorkerOwnershipConfig(
				t, databaseURL, extensionRoot, builtinRoot,
				redisAddress, redisPassword, test.safeMode,
			)
			var queryInvalidation *productionQueryInvalidationRuntime
			if test.embedded {
				queryInvalidation = newEmbeddedWorkerQueryInvalidationRuntime(cfg, installationID, logger)
			} else {
				queryInvalidation = newStandaloneWorkerQueryInvalidationRuntime(cfg, installationID, logger)
			}
			if queryInvalidation == nil || (test.safeMode && queryInvalidation.Invalidator() != nil) ||
				(!test.safeMode && queryInvalidation.Invalidator() == nil) {
				t.Fatalf("%s Query invalidation runtime=%#v", test.name, queryInvalidation)
			}

			deps := workerRuntimeDeps{QueryInvalidation: queryInvalidation}
			var injected *countingWorkerRuntime
			standaloneBefore := len(standaloneRuntimes)
			if test.embedded {
				injected = &countingWorkerRuntime{}
				deps.ExtensionRuntime = injected
			}
			worker, err := newWorkerWithPool(cfg, pool, logger, deps)
			if err != nil {
				t.Fatalf("build %s Query worker: %v", test.name, err)
			}
			var ownedRuntime *countingWorkerRuntime
			if test.embedded {
				if len(standaloneRuntimes) != standaloneBefore {
					t.Fatal("embedded Query worker constructed a standalone plugin runtime")
				}
			} else {
				if len(standaloneRuntimes) != standaloneBefore+1 {
					t.Fatal("standalone Query worker did not construct exactly one plugin runtime")
				}
				ownedRuntime = standaloneRuntimes[len(standaloneRuntimes)-1]
			}

			expectedEvent := river.EventKindJobCompleted
			if test.safeMode {
				expectedEvent = river.EventKindJobSnoozed
			}
			events, cancelEvents := worker.Client.Subscribe(expectedEvent)
			defer cancelEvents()
			started := false
			var jobID int64
			defer func() {
				if started {
					stopP7QueryWorkerOwnership(t, worker)
				}
				worker.Close()
				if jobID != 0 {
					cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if _, err := pool.Exec(cleanupCtx, `DELETE FROM river_job WHERE id = $1`, jobID); err != nil {
						t.Errorf("delete Query ownership job: %v", err)
					}
				}
			}()
			owner := "p7.query-worker." + strings.ReplaceAll(test.name, " ", "-")
			args, err := queryregistryjobs.NewInvalidateResultCacheArgs(owner, []string{owner + ".items"})
			if err != nil {
				t.Fatal(err)
			}
			inserted, err := supportjobs.NewDispatcher(worker.Client).Enqueue(ctx, args, args.QueueOpts())
			if err != nil {
				t.Fatalf("enqueue %s Query invalidation: %v", test.name, err)
			}
			if inserted == nil || inserted.Job == nil {
				t.Fatalf("enqueue %s Query invalidation returned no durable job", test.name)
			}
			jobID = inserted.Job.ID
			if err := worker.Start(ctx); err != nil {
				t.Fatalf("start %s Query worker: %v", test.name, err)
			}
			started = true
			waitP7QueryWorkerOwnershipEvent(t, ctx, events, expectedEvent, jobID)
			stopP7QueryWorkerOwnership(t, worker)
			started = false
			wantState := "completed"
			if test.safeMode {
				wantState = "scheduled"
			}
			requireP7QueryWorkerOwnershipJobState(t, pool, jobID, wantState)
			var authority *productionQueryResultCacheRuntime
			if !test.safeMode {
				authority = currentP7QueryWorkerAuthority(t, queryInvalidation)
				normalAuthorities = append(normalAuthorities, authority)
			}
			worker.Close()
			worker.Close()

			if authority != nil {
				if err := authority.client.Ping(ctx).Err(); !errors.Is(err, redis.ErrClosed) {
					t.Fatalf("%s worker Query Redis client remained open: %v", test.name, err)
				}
				if err := apiCache.client.Ping(ctx).Err(); err != nil {
					t.Fatalf("%s worker close poisoned API Query cache client: %v", test.name, err)
				}
				if _, err := apiCache.InvalidateOwnerTags(ctx, owner, []string{owner + ".api-liveness"}); err != nil {
					t.Fatalf("%s worker close poisoned API Query cache authority: %v", test.name, err)
				}
			}
			if injected != nil && injected.closeCalls != 0 {
				t.Fatalf("embedded Query worker closed shared runtime %d times", injected.closeCalls)
			}
			if ownedRuntime != nil && ownedRuntime.closeCalls != 1 {
				t.Fatalf("standalone Query worker runtime closes=%d, want 1", ownedRuntime.closeCalls)
			}
			if (test.embedded || test.safeMode) && worker.Failures() != nil {
				t.Fatalf("%s Query worker exposed a false coordinator failure source", test.name)
			}
		})
	}
	if len(normalAuthorities) != 2 || normalAuthorities[0] == normalAuthorities[1] ||
		normalAuthorities[0].client == normalAuthorities[1].client ||
		normalAuthorities[0].client == apiCache.client || normalAuthorities[1].client == apiCache.client {
		t.Fatal("API, embedded worker, and standalone worker shared Query Redis authority")
	}
}

func p7QueryWorkerOwnershipDatabaseURL(t *testing.T, baseURL, seed string) string {
	t.Helper()
	poolConfig, err := pgxpool.ParseConfig(baseURL)
	if err != nil {
		t.Fatalf("parse joined Query database URL: %v", err)
	}
	digest := sha256.Sum256([]byte(seed))
	poolConfig.ConnConfig.Database = "sforum_query_joined_" + hex.EncodeToString(digest[:16])
	return poolConfig.ConnString()
}

func p7QueryRedisRunID(t *testing.T, ctx context.Context, client *redis.Client) string {
	t.Helper()
	if client == nil {
		t.Fatal("joined Query Redis client is nil")
	}
	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		t.Fatalf("read joined Query Redis process identity: %v", err)
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "run_id:") {
			runID := strings.TrimSpace(strings.TrimPrefix(line, "run_id:"))
			if runID != "" {
				return runID
			}
		}
	}
	t.Fatal("joined Query Redis INFO omitted run_id")
	return ""
}

func currentP7QueryWorkerAuthority(
	t *testing.T,
	runtime *productionQueryInvalidationRuntime,
) *productionQueryResultCacheRuntime {
	t.Helper()
	if runtime == nil || runtime.invalidator == nil {
		t.Fatal("production Query worker invalidator is unavailable")
	}
	runtime.invalidator.mu.Lock()
	current := runtime.invalidator.current
	runtime.invalidator.mu.Unlock()
	authority, ok := current.(*productionQueryResultCacheRuntime)
	if !ok || authority == nil || authority.cache == nil || authority.client == nil {
		t.Fatalf("production Query worker current authority=%T, want active Redis authority", current)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	wantRunID := strings.TrimSpace(os.Getenv("SFORUM_QUERY_CACHE_JOINED_EXPECTED_REDIS_RUN_ID"))
	if got := p7QueryRedisRunID(t, ctx, authority.client); got != wantRunID {
		t.Fatalf("production Query worker Redis run_id=%q, want %q", got, wantRunID)
	}
	return authority
}

func requireP7QueryWorkerOwnershipJobState(
	t *testing.T,
	pool *pgxpool.Pool,
	jobID int64,
	wantState string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var state string
		var attempt int
		var finalized bool
		var scheduledFuture bool
		err := pool.QueryRow(ctx, `
			SELECT state::text, attempt, finalized_at IS NOT NULL,
			       scheduled_at > clock_timestamp() + interval '30 seconds'
			FROM river_job
			WHERE id = $1
		`, jobID).Scan(&state, &attempt, &finalized, &scheduledFuture)
		valid := err == nil && state == wantState
		if wantState == "completed" {
			valid = valid && attempt == 1 && finalized
		} else {
			valid = valid && attempt == 0 && !finalized && scheduledFuture
		}
		if valid {
			return
		}
		if ctx.Err() != nil {
			t.Fatalf("Query ownership job %d state=%q attempt=%d finalized=%t scheduled_future=%t err=%v, want %q",
				jobID, state, attempt, finalized, scheduledFuture, err, wantState)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func p7QueryWorkerOwnershipConfig(
	t *testing.T,
	databaseURL string,
	extensionRoot string,
	builtinRoot string,
	redisAddress string,
	redisPassword string,
	safeMode bool,
) config.Config {
	t.Helper()
	cfg := p7QueryRedisOwnershipConfig(redisAddress, redisPassword, safeMode)
	cfg.AppEnv = "test"
	cfg.DatabaseURL = databaseURL
	cfg.ExtensionRoot = extensionRoot
	cfg.BuiltinExtensionRoot = builtinRoot
	cfg.WorkerShutdownTimeout = 5 * time.Second
	cfg.JobQueueCriticalWorkers = 1
	cfg.JobQueueDefaultWorkers = 1
	cfg.JobQueueSearchWorkers = 1
	cfg.JobQueueMailWorkers = 1
	cfg.JobQueueNotificationsWorkers = 1
	cfg.JobQueueMaintenanceWorkers = 1
	return cfg
}

func p7QueryRedisOwnershipConfig(address, password string, safeMode bool) config.Config {
	return config.Config{
		SafeMode: safeMode, RedisAddr: address, RedisPassword: password,
		RedisPoolSize: 4, RedisDialTimeout: 2 * time.Second,
		RedisReadTimeout: 4 * time.Second, RedisWriteTimeout: 4 * time.Second,
		RedisConnMaxIdleTime: time.Minute, RedisConnMaxLifetime: 2 * time.Minute,
	}
}

func waitP7QueryWorkerOwnershipEvent(
	t *testing.T,
	ctx context.Context,
	events <-chan *river.Event,
	wantKind river.EventKind,
	jobID int64,
) {
	t.Helper()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("Query worker ownership event subscription closed")
			}
			if event != nil && event.Job != nil && event.Job.ID == jobID {
				if event.Kind != wantKind {
					t.Fatalf("Query worker event kind=%q, want %q", event.Kind, wantKind)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("wait Query worker ownership event: %v", ctx.Err())
		}
	}
}

func stopP7QueryWorkerOwnership(t *testing.T, worker *Worker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := worker.Stop(ctx); err != nil {
		t.Errorf("stop Query worker ownership gate: %v", err)
	}
}
