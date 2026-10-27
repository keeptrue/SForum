package hostapi_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/keeptrue/sforum/apps/api/internal/testdb"
)

const (
	p7QueryCacheJoinedPhaseEnv = "SFORUM_QUERY_CACHE_JOINED_PHASE"
	p7QueryCacheJoinedSeedEnv  = "SFORUM_QUERY_CACHE_JOINED_SEED"
)

func TestP7QueryCacheJoinedPreMarkerOwnershipCleanup(t *testing.T) {
	baseURL := testdb.IsolatedURL(t)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		t.Fatal(err)
	}
	token := hex.EncodeToString(random)
	seed := "pre-marker-cleanup-" + token
	t.Setenv(p7QueryCacheJoinedOwnershipTokenEnv, token)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	fixture := newP7QueryCacheJoinedFixture(
		ctx, baseURL, p7QueryCacheJoinedDatabaseURL(t, baseURL, p7QueryCacheJoinedDatabaseName(seed)),
		p7QueryCacheJoinedDatabaseName(seed), seed,
	)
	fixture.setIdentifiers(t)
	t.Cleanup(func() {
		if err := p7QueryCacheJoinedDropDatabase(baseURL, fixture.databaseName); err != nil {
			t.Errorf("fallback pre-marker database cleanup: %v", err)
		}
		if err := p7QueryCacheJoinedCleanupRoles(baseURL, fixture.databaseName, fixture.identifiers); err != nil {
			t.Errorf("fallback pre-marker role cleanup: %v", err)
		}
	})
	p7QueryCacheJoinedCreateDatabase(t, ctx, baseURL, fixture.databaseName)
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{fixture.identifiers.OwnerRole}.Sanitize()+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{fixture.identifiers.RuntimeRole}.Sanitize()+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "GRANT "+pgx.Identifier{fixture.identifiers.OwnerRole}.Sanitize()+" TO "+pgx.Identifier{fixture.identifiers.RuntimeRole}.Sanitize()); err != nil {
		t.Fatal(err)
	}

	cleanupP7QueryCacheJoinedFixture(t, ctx, baseURL, seed)
	if p7QueryCacheJoinedDatabaseExists(t, ctx, baseURL, fixture.databaseName) {
		t.Fatal("pre-marker cleanup retained its token-bound database")
	}
	var ownerExists, runtimeExists bool
	if err := admin.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1),
		       EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $2)
	`, fixture.identifiers.OwnerRole, fixture.identifiers.RuntimeRole).Scan(&ownerExists, &runtimeExists); err != nil {
		t.Fatal(err)
	}
	if ownerExists || runtimeExists {
		t.Fatalf("pre-marker cleanup retained roles owner=%t runtime=%t", ownerExists, runtimeExists)
	}
}

// TestP7QueryRegistryMutationCacheRestartJoined is an explicit two-process
// gate. The repository runner executes seed, restarts the dedicated Redis
// process, then executes verify with the same seed.
func TestP7QueryRegistryMutationCacheRestartJoined(t *testing.T) {
	phase := strings.TrimSpace(os.Getenv(p7QueryCacheJoinedPhaseEnv))
	seed := strings.TrimSpace(os.Getenv(p7QueryCacheJoinedSeedEnv))
	if phase == "" && seed == "" {
		t.Skip("set SFORUM_QUERY_CACHE_JOINED_PHASE and SFORUM_QUERY_CACHE_JOINED_SEED")
	}
	if seed == "" {
		t.Fatalf("%s is required", p7QueryCacheJoinedSeedEnv)
	}
	requireP7QueryCacheJoinedSeedOwnership(t, seed)
	baseURL := testdb.RequireIsolatedURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	switch phase {
	case "seed":
		runP7QueryCacheJoinedSeed(t, ctx, baseURL, seed)
	case "verify":
		runP7QueryCacheJoinedVerify(t, ctx, baseURL, seed)
	case "cleanup":
		cleanupP7QueryCacheJoinedFixture(t, ctx, baseURL, seed)
	default:
		t.Fatalf("unsupported %s=%q", p7QueryCacheJoinedPhaseEnv, phase)
	}
}

func runP7QueryCacheJoinedSeed(t *testing.T, ctx context.Context, baseURL, seed string) {
	t.Helper()
	fixture := newP7QueryCacheJoinedSeedFixture(t, ctx, baseURL, seed)
	apiClient := newP7QueryCacheJoinedRedisClient(t)
	workerClient := newP7QueryCacheJoinedRedisClient(t)
	t.Cleanup(func() {
		_ = workerClient.Close()
		_ = apiClient.Close()
	})
	if apiClient == workerClient {
		t.Fatal("API and worker shared the same Redis client")
	}
	seedRunID := p7QueryCacheJoinedRedisRunID(t, ctx, apiClient)
	fixture.recordRedisRunID(t, seedRunID)
	apiCache := newP7QueryCacheJoinedRedisCache(t, ctx, apiClient, seed)
	workerCache := newP7QueryCacheJoinedRedisCache(t, ctx, workerClient, seed)
	execution := fixture.buildExecution(t, apiCache)

	executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "before", false, 1)
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "before", true, 1)
	stale := executeP7QueryCacheJoined(t, execution, fixture.queryID, "zh-CN", "before", false, 2)
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "zh-CN", "before", true, 2)
	staleKey, staleDigest := snapshotP7QueryCacheJoinedRedisEnvelope(
		t, ctx, apiClient, stale.CacheKey, "before",
	)

	jobID := fixture.executeMutation(t)
	runP7QueryCacheJoinedWorker(t, ctx, fixture.pool, workerCache, jobID)
	// Invalidation advances the tag epoch; it does not erase the stale physical
	// envelope. Keeping the exact bytes makes the post-restart miss meaningful.
	requireP7QueryCacheJoinedRedisValue(t, ctx, apiClient, staleKey, staleDigest)
	if err := workerClient.Close(); err != nil {
		t.Fatalf("close worker Query cache client: %v", err)
	}
	if err := workerClient.Ping(ctx).Err(); !errors.Is(err, redis.ErrClosed) {
		t.Fatalf("worker Query cache client remained open: %v", err)
	}
	if err := apiClient.Ping(ctx).Err(); err != nil {
		t.Fatalf("worker close poisoned API Query cache client: %v", err)
	}

	live := executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "after", false, 3)
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "after", true, 3)
	liveKey, liveDigest := snapshotP7QueryCacheJoinedRedisEnvelope(
		t, ctx, apiClient, live.CacheKey, "after",
	)
	fixture.recordRedisEvidence(t, p7QueryCacheJoinedRedisEvidence{
		staleKey: staleKey, staleDigest: staleDigest,
		liveKey: liveKey, liveDigest: liveDigest,
	})
	fixture.assertDurableEvidence(t)

	// The runner-owned Redis uses appendfsync=always. Retain the byte-bound stale
	// and live envelopes plus the durable semantic epoch for the restart phase.
	fixture.retainForRestart()
}

func runP7QueryCacheJoinedVerify(t *testing.T, ctx context.Context, baseURL, seed string) {
	t.Helper()
	fixture := openP7QueryCacheJoinedVerifyFixture(t, ctx, baseURL, seed)
	client := newP7QueryCacheJoinedRedisClient(t)
	t.Cleanup(func() { _ = client.Close() })
	fixture.requireRedisRestartEvidence(
		t, ctx, client, p7QueryCacheJoinedRedisRunID(t, ctx, client),
	)
	cache := newP7QueryCacheJoinedRedisCache(t, ctx, client, seed)
	execution := fixture.buildExecution(t, cache)

	// The uninvalidated entry must survive the same restart and hit without a
	// provider call; otherwise an empty Redis instance could make this gate pass.
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "after", true, 0)
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "en-US", "after", true, 0)

	// The exact zh-CN stale bytes were verified before Activate. Its persisted
	// older tag digest must now miss because the worker's semantic epoch survived.
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "zh-CN", "after", false, 1)
	executeP7QueryCacheJoined(t, execution, fixture.queryID, "zh-CN", "after", true, 1)
	fixture.assertDurableEvidence(t)
}
