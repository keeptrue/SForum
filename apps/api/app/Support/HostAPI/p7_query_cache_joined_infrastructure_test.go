package hostapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/riverqueue/river"
	queryregistryjobs "github.com/keeptrue/sforum/apps/api/app/Jobs/QueryRegistry"
	extensionsruntime "github.com/keeptrue/sforum/apps/api/app/Support/Extensions"
	supportjobs "github.com/keeptrue/sforum/apps/api/app/Support/Jobs"
	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/database/coreauthority"
)

func runP7QueryCacheJoinedWorker(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	invalidator queryregistry.SemanticCacheInvalidator,
	jobID int64,
) {
	t.Helper()
	registry := supportjobs.NewRegistry()
	queryregistryjobs.Register(registry, invalidator, nil)
	workers, err := registry.Build()
	if err != nil {
		t.Fatalf("build joined Query workers: %v", err)
	}
	client, err := supportjobs.NewClient(pool, supportjobs.Config{
		CriticalWorkers:      1,
		DefaultWorkers:       1,
		SearchWorkers:        1,
		MailWorkers:          1,
		NotificationsWorkers: 1,
		MaintenanceWorkers:   1,
	}, workers)
	if err != nil {
		t.Fatalf("create joined Query worker client: %v", err)
	}
	events, cancelEvents := client.Subscribe(river.EventKindJobCompleted)
	defer cancelEvents()
	if err := supportjobs.Start(ctx, client); err != nil {
		t.Fatalf("start joined Query worker: %v", err)
	}
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := supportjobs.Stop(stopCtx, client); err != nil {
			t.Errorf("stop joined Query worker: %v", err)
		}
	}
	defer stop()

	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatal("joined Query worker event subscription closed before completion")
			}
			if event != nil && event.Job != nil && event.Job.ID == jobID {
				stop()
				var state string
				if err := pool.QueryRow(ctx, `SELECT state::text FROM river_job WHERE id = $1`, jobID).Scan(&state); err != nil {
					t.Fatalf("inspect joined Query completed job: %v", err)
				}
				if state != "completed" {
					t.Fatalf("joined Query job state=%q, want completed", state)
				}
				return
			}
		case <-ctx.Done():
			t.Fatalf("wait joined Query worker: %v", ctx.Err())
		}
	}
}

func newP7QueryCacheJoinedRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	address := strings.TrimSpace(os.Getenv(p7QueryCacheJoinedRedisAddressEnv))
	if address == "" {
		t.Fatalf("%s is required", p7QueryCacheJoinedRedisAddressEnv)
	}
	client := redis.NewClient(&redis.Options{
		Addr: address, Password: os.Getenv(p7QueryCacheJoinedRedisPasswordEnv), DB: 0,
		DialTimeout: 2 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 5 * time.Second,
		MaxRetries: -1, ContextTimeoutEnabled: true,
	})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Fatalf("dedicated joined Query Redis unavailable: %v", err)
	}
	expectedRunID := strings.TrimSpace(os.Getenv(p7QueryCacheJoinedExpectedRedisRunIDEnv))
	if expectedRunID == "" {
		_ = client.Close()
		t.Fatalf("%s is required", p7QueryCacheJoinedExpectedRedisRunIDEnv)
	}
	if actualRunID := p7QueryCacheJoinedRedisRunID(t, ctx, client); actualRunID != expectedRunID {
		_ = client.Close()
		t.Fatalf("joined Query Redis endpoint does not belong to the runner container: run_id=%q want=%q", actualRunID, expectedRunID)
	}
	return client
}

func newP7QueryCacheJoinedRedisCache(
	t *testing.T,
	ctx context.Context,
	client *redis.Client,
	seed string,
) *queryregistry.RedisQueryResultCache {
	t.Helper()
	digest := sha256.Sum256([]byte("installation\x00" + seed))
	cache, err := queryregistry.NewRedisQueryResultCache(client, hex.EncodeToString(digest[:]))
	if err != nil {
		t.Fatal(err)
	}
	if err := cache.Activate(ctx); err != nil {
		t.Fatalf("activate joined Query cache: %v", err)
	}
	return cache
}

func p7QueryCacheJoinedRedisRunID(t *testing.T, ctx context.Context, client *redis.Client) string {
	t.Helper()
	info, err := client.Info(ctx, "server").Result()
	if err != nil {
		t.Fatalf("read joined Query Redis process identity: %v", err)
	}
	for _, line := range strings.Split(info, "\n") {
		if strings.HasPrefix(line, "run_id:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "run_id:"))
		}
	}
	t.Fatal("joined Query Redis INFO omitted run_id")
	return ""
}

func p7QueryCacheJoinedOwnershipToken(t *testing.T) string {
	t.Helper()
	token := strings.TrimSpace(os.Getenv(p7QueryCacheJoinedOwnershipTokenEnv))
	decoded, err := hex.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("%s must be 32 random bytes encoded as lowercase hex", p7QueryCacheJoinedOwnershipTokenEnv)
	}
	if token != strings.ToLower(token) {
		t.Fatalf("%s must use lowercase hex", p7QueryCacheJoinedOwnershipTokenEnv)
	}
	return token
}

func requireP7QueryCacheJoinedSeedOwnership(t *testing.T, seed string) string {
	t.Helper()
	token := p7QueryCacheJoinedOwnershipToken(t)
	if !strings.HasSuffix(seed, "-"+token) {
		t.Fatalf("%s must include the runner ownership token", p7QueryCacheJoinedSeedEnv)
	}
	return token
}

func p7QueryCacheJoinedRequest(queryID, locale string) queryregistry.PlanRequest {
	return queryregistry.PlanRequest{
		QueryID: queryID, Fields: []string{"name"}, Locale: locale, Scope: "forum.main",
	}
}

func executeP7QueryCacheJoined(
	t *testing.T,
	execution *p7QueryCacheJoinedExecution,
	queryID string,
	locale string,
	wantName string,
	wantHit bool,
	wantCalls int32,
) queryregistry.QueryResult {
	t.Helper()
	result, err := execution.runtime.Execute(t.Context(), p7QueryCacheJoinedRequest(queryID, locale))
	if err != nil || result.CacheHit != wantHit || len(result.Rows) != 1 ||
		result.Rows[0]["name"] != wantName || execution.providerCalls.Load() != wantCalls {
		t.Fatalf("joined Query locale=%s result=%#v err=%v provider_calls=%d want_hit=%t want_calls=%d",
			locale, result, err, execution.providerCalls.Load(), wantHit, wantCalls)
	}
	return result
}

func p7QueryCacheJoinedDatabaseName(seed string) string {
	return "sforum_query_joined_" + p7QueryCacheJoinedSuffix(seed)
}

func p7QueryCacheJoinedSuffix(seed string) string {
	digest := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(digest[:16])
}

func p7QueryCacheJoinedDatabaseURL(t *testing.T, baseURL, databaseName string) string {
	t.Helper()
	parsed, err := url.Parse(baseURL)
	if err != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Host == "" {
		t.Fatalf("SFORUM_TEST_DATABASE_URL must be a PostgreSQL URL: %v", err)
	}
	parsed.Path = "/" + databaseName
	parsed.RawPath = ""
	return parsed.String()
}

func p7QueryCacheJoinedCreateDatabase(t *testing.T, ctx context.Context, baseURL, databaseName string) {
	t.Helper()
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("open joined Query database administrator: %v", err)
	}
	defer admin.Close()
	if p7QueryCacheJoinedDatabaseExistsWithPool(t, ctx, admin, databaseName) {
		t.Fatalf("isolated P7 Query database %q already exists; use a new seed or run verify cleanup", databaseName)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		t.Fatalf("create isolated P7 Query database: %v", err)
	}
}

func p7QueryCacheJoinedDatabaseExists(t *testing.T, ctx context.Context, baseURL, databaseName string) bool {
	t.Helper()
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatalf("open joined Query database administrator: %v", err)
	}
	defer admin.Close()
	return p7QueryCacheJoinedDatabaseExistsWithPool(t, ctx, admin, databaseName)
}

func p7QueryCacheJoinedDatabaseExistsWithPool(
	t *testing.T,
	ctx context.Context,
	admin *pgxpool.Pool,
	databaseName string,
) bool {
	t.Helper()
	var exists bool
	if err := admin.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, databaseName).Scan(&exists); err != nil {
		t.Fatalf("inspect isolated P7 Query database: %v", err)
	}
	return exists
}

func p7QueryCacheJoinedDropDatabase(baseURL, databaseName string) error {
	if baseURL == "" || databaseName == "" {
		return nil
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		admin, err := pgxpool.New(ctx, baseURL)
		if err == nil {
			_, err = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+pgx.Identifier{databaseName}.Sanitize()+" WITH (FORCE)")
			admin.Close()
		}
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
	return fmt.Errorf("drop isolated P7 Query database after retries: %w", lastErr)
}

func p7QueryCacheJoinedRevokeGrant(fixture *p7QueryCacheJoinedFixture) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	registry := extensionsruntime.NewPostgresExtensionDatabaseRegistry(fixture.pool, nil)
	return registry.RevokeOwnSchema(ctx, extensionsruntime.ExtensionDatabaseGrantRequest{
		Artifact: fixture.artifact, ActorUserID: 1702, AuditEventID: 1802,
	})
}

func p7QueryCacheJoinedCleanupRoles(
	baseURL string,
	databaseName string,
	identifiers extensionsruntime.ExtensionDatabaseIdentifiers,
) error {
	coreOwnerRole, err := coreauthority.OwnerRoleName(databaseName)
	if err != nil {
		return fmt.Errorf("derive joined Query Core owner role: %w", err)
	}
	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		err := func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			admin, err := pgxpool.New(ctx, baseURL)
			if err != nil {
				return err
			}
			defer admin.Close()
			var sessionRole string
			var ownerExists, runtimeExists, coreOwnerExists bool
			if err := admin.QueryRow(ctx, `
					SELECT current_user,
					       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
					       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2),
					       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $3)
				`, identifiers.OwnerRole, identifiers.RuntimeRole, coreOwnerRole).
				Scan(&sessionRole, &ownerExists, &runtimeExists, &coreOwnerExists); err != nil {
				return err
			}
			if ownerExists && runtimeExists {
				if _, err := admin.Exec(ctx, "REVOKE "+pgx.Identifier{identifiers.OwnerRole}.Sanitize()+" FROM "+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()); err != nil {
					return err
				}
			}
			if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{identifiers.RuntimeRole}.Sanitize()); err != nil {
				return err
			}
			if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{identifiers.OwnerRole}.Sanitize()); err != nil {
				return err
			}
			if coreOwnerExists {
				if _, err := admin.Exec(ctx, "REVOKE "+pgx.Identifier{coreOwnerRole}.Sanitize()+" FROM "+pgx.Identifier{sessionRole}.Sanitize()); err != nil {
					return err
				}
			}
			if _, err := admin.Exec(ctx, "DROP ROLE IF EXISTS "+pgx.Identifier{coreOwnerRole}.Sanitize()); err != nil {
				return err
			}
			if err := admin.QueryRow(ctx, `
					SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
					       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2),
					       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $3)
				`, identifiers.OwnerRole, identifiers.RuntimeRole, coreOwnerRole).
				Scan(&ownerExists, &runtimeExists, &coreOwnerExists); err != nil {
				return err
			}
			if ownerExists || runtimeExists || coreOwnerExists {
				return fmt.Errorf("owned roles remain: owner=%t runtime=%t core_owner=%t", ownerExists, runtimeExists, coreOwnerExists)
			}
			return nil
		}()
		if err == nil {
			return nil
		}
		lastErr = err
		time.Sleep(time.Duration(attempt) * 100 * time.Millisecond)
	}
	return fmt.Errorf("drop joined Query roles after retries: %w", lastErr)
}

func cleanupP7QueryCacheJoinedFixture(
	t *testing.T,
	ctx context.Context,
	baseURL string,
	seed string,
) {
	t.Helper()
	token := requireP7QueryCacheJoinedSeedOwnership(t, seed)
	databaseName := p7QueryCacheJoinedDatabaseName(seed)
	fixture := newP7QueryCacheJoinedFixture(
		ctx, baseURL, p7QueryCacheJoinedDatabaseURL(t, baseURL, databaseName), databaseName, seed,
	)
	fixture.setIdentifiers(t)
	if !p7QueryCacheJoinedDatabaseExists(t, ctx, baseURL, databaseName) {
		if err := p7QueryCacheJoinedCleanupRoles(baseURL, databaseName, fixture.identifiers); err != nil {
			t.Fatalf("cleanup joined Query roles without retained database: %v", err)
		}
		return
	}
	pool, err := pgxpool.New(ctx, fixture.databaseURL)
	if err != nil {
		t.Fatalf("open joined Query cleanup database: %v", err)
	}
	finished := false
	defer func() {
		if !finished {
			pool.Close()
		}
	}()
	fixture.pool = pool
	matched, err := fixture.matchesOwnershipEvidence(token)
	if err != nil {
		t.Fatalf("validate joined Query cleanup ownership: %v", err)
	}
	fixture.cleanupAuthorized = true
	if !matched {
		t.Log("joined Query cleanup recovered a token-bound database before its ownership marker committed")
	}
	var restoreErr error
	var extensionsTable *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.extensions')::text`).Scan(&extensionsTable); err != nil {
		restoreErr = err
	} else if extensionsTable != nil {
		err := pool.QueryRow(ctx, `
			SELECT ev.id, tg.id
			FROM extensions e
			JOIN extension_versions ev ON ev.id = e.active_version_id
			JOIN extension_trust_grants tg
			  ON tg.extension_id = e.id
			 AND tg.extension_version = ev.version
			 AND tg.package_digest = ev.package_digest
			 AND tg.action = 'enable'
			 AND tg.revoked_at IS NULL
			WHERE e.id = $1
		`, fixture.extensionID).Scan(&fixture.artifact.VersionID, &fixture.trustGrantID)
		if err == nil {
			fixture.grantProvisioned = true
		} else if !errors.Is(err, pgx.ErrNoRows) {
			restoreErr = err
		}
	}
	fixture.finish(t)
	finished = true
	if restoreErr != nil {
		t.Errorf("restore joined Query cleanup grant: %v", restoreErr)
	}
}
