package queryregistry

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"
	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
)

const (
	// RedisQueryResultCacheTTL is Host-owned. Plugins declare semantic tags but
	// never control retention or Redis key material.
	RedisQueryResultCacheTTL = 2 * time.Minute

	redisQueryCacheEnvelopeVersion   = "sforum.query-result-redis@3"
	redisQueryCacheMaximumEnvelope   = maximumResultBytes + 256<<10
	redisQueryCacheMaximumJSONDepth  = maximumResultJSONDepth + 8
	redisQueryCacheMaximumTags       = maxCacheTagsPerQuery * 2
	redisQueryCacheLoadAttempts      = 3
	redisQueryCacheWAITAOFTimeout    = 2 * time.Second
	redisQueryCacheTTLGuard          = 5 * time.Second
	redisQueryCacheMaximumGeneration = uint64(9_007_199_254_740_991)
	redisQueryCacheInitialEpoch      = "1"
	redisQueryCacheActivationMarker  = redisQueryCacheEnvelopeVersion
	redisQueryCacheTemporaryAttempts = 3

	redisQueryCacheTagTTL = RedisQueryResultCacheTTL + maximumExecutionTimeout +
		redisQueryCacheWAITAOFTimeout + redisQueryCacheTTLGuard
)

const (
	redisQueryCacheStateNew uint32 = iota
	redisQueryCacheStateActive
	redisQueryCacheStateFailed
)

var (
	ErrCacheDurability = errors.New("query registry cache write was not confirmed by Redis AOF")
	ErrCacheCapability = errors.New("query registry cache Redis capabilities are unavailable")
	errRedisCacheRetry = errors.New("query registry cache changed during bounded read")
)

type RedisQueryResultCache struct {
	client          *redis.Client
	root            string
	valuePrefix     string
	tagPrefix       string
	temporaryPrefix string
	markerKey       string
	epochKey        string
	ttl             time.Duration
	tagTTL          time.Duration
	waitAOFTimeout  time.Duration
	state           atomic.Uint32
}

type redisQueryResultCacheFence struct {
	root        string
	cacheKey    string
	epoch       string
	physical    []string
	generations []string
	tagDigest   string
	digest      string
}

func (redisQueryResultCacheFence) QueryResultCacheFenceToken() {}

type redisQueryCacheEnvelope struct {
	Version   string                 `json:"version"`
	TagDigest string                 `json:"tagDigest"`
	Result    redisCachedQueryResult `json:"result"`
}

type redisCachedQueryResult struct {
	SchemaVersion    string          `json:"schemaVersion"`
	CacheKey         string          `json:"cacheKey"`
	RegistryRevision uint64          `json:"registryRevision"`
	RegistryDigest   string          `json:"registryDigest"`
	ShapeDigest      string          `json:"shapeDigest"`
	FilterPlan       string          `json:"filterPlan"`
	QueryID          string          `json:"queryId"`
	ContractVersion  string          `json:"contractVersion"`
	PlanVersion      string          `json:"planVersion"`
	ResultSchema     string          `json:"resultSchema"`
	Artifact         Artifact        `json:"artifact"`
	ProviderDigest   string          `json:"providerDigest"`
	Page             QueryResultPage `json:"page"`
	Rows             []QueryRow      `json:"rows"`
	CacheTags        []string        `json:"cacheTags"`
}

func NewRedisQueryResultCache(client *redis.Client, installationID string) (*RedisQueryResultCache, error) {
	if client == nil || !installationidentity.Valid(installationID) {
		return nil, ErrExecutionInvalid
	}
	options := client.Options()
	if !validRedisQueryCacheClientOptions(options, redisQueryCacheWAITAOFTimeout) {
		return nil, ErrCacheCapability
	}
	digest := sha256.Sum256([]byte(redisQueryCacheEnvelopeVersion + "\x00installation\x00" + installationID))
	installationDigest := hex.EncodeToString(digest[:])
	root := "sforum:query-result:v3:{" + installationDigest + "}:"
	return &RedisQueryResultCache{
		client: client, root: root, valuePrefix: root + "value:", tagPrefix: root + "tag:",
		temporaryPrefix: root + "temporary:", markerKey: root + "activation", epochKey: root + "epoch",
		ttl: RedisQueryResultCacheTTL, tagTTL: redisQueryCacheTagTTL,
		waitAOFTimeout: redisQueryCacheWAITAOFTimeout,
	}, nil
}

func validRedisQueryCacheClientOptions(options *redis.Options, waitAOFTimeout time.Duration) bool {
	return options != nil && options.MaxRetries == 0 && options.ContextTimeoutEnabled &&
		options.ReadTimeout >= waitAOFTimeout+time.Second && options.WriteTimeout > 0
}

func (c *RedisQueryResultCache) requireActive() error {
	if c.state.Load() != redisQueryCacheStateActive {
		return ErrCacheCapability
	}
	return nil
}

func (c *RedisQueryResultCache) markActive() error {
	for {
		switch c.state.Load() {
		case redisQueryCacheStateFailed:
			return ErrCacheCapability
		case redisQueryCacheStateActive:
			return nil
		case redisQueryCacheStateNew:
			if c.state.CompareAndSwap(redisQueryCacheStateNew, redisQueryCacheStateActive) {
				return nil
			}
		default:
			c.state.Store(redisQueryCacheStateFailed)
			return ErrCacheCapability
		}
	}
}

func (c *RedisQueryResultCache) failAuthority(err error) error {
	c.state.Store(redisQueryCacheStateFailed)
	return err
}

// LoadQueryResult snapshots the permanent allocator plus every effective tag
// epoch before reading. Missing tag keys inherit the current allocator epoch;
// Store materializes them with a longer TTL before publishing a value.
func (c *RedisQueryResultCache) LoadQueryResult(
	ctx context.Context,
	key string,
	tags []string,
) (CachedQueryResult, QueryResultCacheFence, bool, error) {
	if c == nil || c.client == nil || ctx == nil || !validRedisQueryCacheDigest(key) {
		return CachedQueryResult{}, nil, false, ErrExecutionInvalid
	}
	if err := c.requireActive(); err != nil {
		return CachedQueryResult{}, nil, false, err
	}
	physical, err := c.physicalGenerationKeys(tags)
	if err != nil {
		return CachedQueryResult{}, nil, false, ErrExecutionInvalid
	}
	for attempt := 0; attempt < redisQueryCacheLoadAttempts; attempt++ {
		before, err := c.snapshotFence(ctx, key, physical)
		if err != nil {
			if errors.Is(err, ErrCacheCapability) || errors.Is(err, ErrCachePoisoned) {
				c.failAuthority(err)
			}
			return CachedQueryResult{}, nil, false, err
		}
		encoded, found, err := c.readBoundedEnvelope(ctx, c.valueKey(key))
		if errors.Is(err, errRedisCacheRetry) {
			continue
		}
		if err != nil {
			if errors.Is(err, ErrCachePoisoned) {
				c.failAuthority(err)
			}
			return CachedQueryResult{}, nil, false, err
		}
		after, err := c.snapshotFence(ctx, key, physical)
		if err != nil {
			if errors.Is(err, ErrCacheCapability) || errors.Is(err, ErrCachePoisoned) {
				c.failAuthority(err)
			}
			return CachedQueryResult{}, nil, false, err
		}
		if !sameQueryResultCacheTagSnapshot(before, after) {
			continue
		}
		if !found {
			return CachedQueryResult{}, after, false, nil
		}
		envelope, value, err := c.decodeEnvelope(encoded, key)
		if err != nil || !slices.Equal(value.CacheTags, tags) {
			return CachedQueryResult{}, nil, false, c.failAuthority(ErrCachePoisoned)
		}
		afterMaterial, ok := redisQueryResultCacheFenceValue(after)
		if !ok {
			return CachedQueryResult{}, nil, false, c.failAuthority(ErrCachePoisoned)
		}
		if envelope.TagDigest != afterMaterial.tagDigest {
			return CachedQueryResult{}, after, false, nil
		}
		return value, after, true, nil
	}
	return CachedQueryResult{}, nil, false, errors.New("query result cache changed repeatedly during load")
}

func (c *RedisQueryResultCache) StoreQueryResult(
	ctx context.Context,
	key string,
	value CachedQueryResult,
	tags []string,
	fence QueryResultCacheFence,
) error {
	if c == nil || c.client == nil || ctx == nil || !validRedisQueryCacheDigest(key) ||
		!slices.Equal(tags, value.CacheTags) {
		return ErrExecutionInvalid
	}
	if err := c.requireActive(); err != nil {
		return err
	}
	physical, err := c.physicalGenerationKeys(tags)
	fenceMaterial, fenceOK := redisQueryResultCacheFenceValue(fence)
	if err != nil || !fenceOK || !fenceMaterial.validFor(c.root, key, physical) {
		return ErrExecutionInvalid
	}
	value, err = validateRedisCachedQueryResult(value, key)
	if err != nil {
		return err
	}
	envelope := redisQueryCacheEnvelope{
		Version: redisQueryCacheEnvelopeVersion, TagDigest: fenceMaterial.tagDigest,
		Result: redisCachedQueryResultFromValue(value),
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return ErrExecutionInvalid
	}
	if len(encoded) == 0 || len(encoded) > redisQueryCacheMaximumEnvelope {
		return ErrResultTooLarge
	}

	conn := c.client.Conn()
	defer conn.Close()
	temporaryKey, err := c.storeTemporaryEnvelope(ctx, conn, key, encoded)
	if err != nil {
		return err
	}
	// RENAME consumes the temporary key on success; DEL is harmless there and
	// bounds every poison, inactive, conflict, and transport-error path.
	defer c.cleanupTemporaryEnvelope(temporaryKey, conn)
	keys := make([]string, 0, len(physical)+3)
	keys = append(keys, temporaryKey, c.valueKey(key), c.epochKey)
	keys = append(keys, physical...)
	arguments := make([]any, 0, len(fenceMaterial.generations)+4)
	arguments = append(arguments,
		fenceMaterial.epoch,
		redisQueryCacheMaximumGeneration,
		c.ttl.Milliseconds(),
		c.tagTTL.Milliseconds(),
	)
	for _, generation := range fenceMaterial.generations {
		arguments = append(arguments, generation)
	}
	stored, err := redisQueryCacheFinalizeScript.Run(ctx, conn, keys, arguments...).Int64()
	if redisQueryCacheMarkedPoisoned(err) {
		return c.failAuthority(ErrCachePoisoned)
	}
	if redisQueryCacheMarkedInactive(err) {
		return c.failAuthority(ErrCacheCapability)
	}
	if err != nil {
		return err
	}
	if stored == 0 {
		return ErrCacheFenceConflict
	}
	if stored != 1 {
		return c.failAuthority(ErrCachePoisoned)
	}
	// Cached values and their pinned tags are one reconstructible Lua write.
	// Only the authoritative allocator mutations require synchronous AOF proof.
	return nil
}

// InvalidateTags allocates one installation-wide monotonic epoch and assigns
// it to every requested tag in one MSET. Values are never reverse-scanned.
func (c *RedisQueryResultCache) InvalidateTags(ctx context.Context, tags []string) (uint64, error) {
	if c == nil || c.client == nil || ctx == nil {
		return 0, ErrExecutionInvalid
	}
	if err := c.requireActive(); err != nil {
		return 0, err
	}
	physical, err := c.physicalGenerationKeys(tags)
	if err != nil {
		return 0, ErrExecutionInvalid
	}
	keys := make([]string, 1, len(physical)+1)
	keys[0] = c.epochKey
	keys = append(keys, physical...)
	conn := c.client.Conn()
	defer conn.Close()
	rotated, err := redisQueryCacheInvalidateScript.Run(
		ctx, conn, keys, redisQueryCacheMaximumGeneration, c.tagTTL.Milliseconds(),
	).Int64()
	if redisQueryCacheMarkedPoisoned(err) {
		return 0, c.failAuthority(ErrCachePoisoned)
	}
	if redisQueryCacheMarkedInactive(err) {
		return 0, c.failAuthority(ErrCacheCapability)
	}
	if err != nil {
		return 0, c.failAuthority(err)
	}
	if rotated != int64(len(physical)) {
		return 0, c.failAuthority(ErrCachePoisoned)
	}
	if err := waitForRedisQueryCacheAOF(ctx, conn, c.waitAOFTimeout); err != nil {
		return 0, c.failAuthority(err)
	}
	return uint64(rotated), nil
}

func (c *RedisQueryResultCache) snapshotFence(
	ctx context.Context,
	key string,
	physical []string,
) (QueryResultCacheFence, error) {
	keys := make([]string, 1, len(physical)+1)
	keys[0] = c.epochKey
	keys = append(keys, physical...)
	raw, err := redisQueryCacheSnapshotScript.Run(
		ctx, c.client, keys, redisQueryCacheMaximumGeneration, c.tagTTL.Milliseconds(),
	).StringSlice()
	if redisQueryCacheMarkedPoisoned(err) {
		return nil, ErrCachePoisoned
	}
	if redisQueryCacheMarkedInactive(err) {
		return nil, ErrCacheCapability
	}
	if err != nil {
		return nil, err
	}
	if len(raw) != len(physical)+1 || !validRedisQueryCacheEpoch(raw[0]) {
		return nil, ErrCachePoisoned
	}
	for _, generation := range raw[1:] {
		if !validRedisQueryCacheEpoch(generation) {
			return nil, ErrCachePoisoned
		}
	}
	return newQueryResultCacheFence(c.root, key, raw[0], physical, raw[1:]), nil
}

func (c *RedisQueryResultCache) readBoundedEnvelope(
	ctx context.Context,
	physicalKey string,
) ([]byte, bool, error) {
	encoded, err := c.client.GetRange(ctx, physicalKey, 0, int64(redisQueryCacheMaximumEnvelope)).Bytes()
	if redisQueryCacheMarkedPoisoned(err) {
		return nil, false, ErrCachePoisoned
	}
	if err != nil {
		return nil, false, err
	}
	if len(encoded) > redisQueryCacheMaximumEnvelope {
		return nil, false, ErrCachePoisoned
	}
	if len(encoded) > 0 {
		ttl, err := c.client.PTTL(ctx, physicalKey).Result()
		if err != nil {
			return nil, false, err
		}
		switch ttl {
		case -2, 0:
			return nil, false, errRedisCacheRetry
		case -1:
			return nil, false, ErrCachePoisoned
		}
		if ttl < -2 {
			return nil, false, ErrCachePoisoned
		}
		if ttl > c.ttl {
			return nil, false, ErrCachePoisoned
		}
		return encoded, true, nil
	}
	var length, exists *redis.IntCmd
	_, err = c.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
		length = pipe.StrLen(ctx, physicalKey)
		exists = pipe.Exists(ctx, physicalKey)
		return nil
	})
	if redisQueryCacheMarkedPoisoned(err) {
		return nil, false, ErrCachePoisoned
	}
	if err != nil {
		return nil, false, err
	}
	if length.Val() > 0 {
		return nil, false, errRedisCacheRetry
	}
	if exists.Val() == 0 {
		return nil, false, nil
	}
	return nil, false, ErrCachePoisoned
}

func (c *RedisQueryResultCache) storeTemporaryEnvelope(
	ctx context.Context,
	conn *redis.Conn,
	key string,
	encoded []byte,
) (string, error) {
	for attempt := 0; attempt < redisQueryCacheTemporaryAttempts; attempt++ {
		token := make([]byte, 16)
		if _, err := rand.Read(token); err != nil {
			return "", err
		}
		physical := c.temporaryPrefix + redisQueryCacheHash("temporary", key+"\x00"+hex.EncodeToString(token))
		stored, err := conn.SetNX(ctx, physical, encoded, c.tagTTL).Result()
		if err != nil {
			return "", err
		}
		if stored {
			return physical, nil
		}
	}
	return "", errors.New("query result cache could not allocate a temporary key")
}

func (c *RedisQueryResultCache) cleanupTemporaryEnvelope(key string, conn *redis.Conn) {
	c.cleanupRedisQueryCacheKeys(conn, key)
}

func (c *RedisQueryResultCache) cleanupRedisQueryCacheKeys(conn *redis.Conn, keys ...string) {
	if c == nil || conn == nil || len(keys) == 0 {
		return
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = conn.Del(cleanupCtx, keys...).Err()
}

func waitForRedisQueryCacheAOF(ctx context.Context, conn *redis.Conn, timeout time.Duration) error {
	if ctx == nil || conn == nil || timeout <= 0 {
		return ErrExecutionInvalid
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout+time.Second)
	defer cancel()
	command := redis.NewCmd(waitCtx, "WAITAOF", 1, 0, timeout.Milliseconds())
	if err := conn.Process(waitCtx, command); err != nil {
		return errors.Join(ErrCacheDurability, err)
	}
	reply, err := command.Int64Slice()
	if err == nil {
		err = validateRedisQueryCacheAOFReply(reply)
	}
	if err != nil {
		return errors.Join(ErrCacheDurability, err)
	}
	return nil
}

func validateRedisQueryCacheAOFReply(reply []int64) error {
	if len(reply) != 2 || reply[0] < 1 || reply[1] < 0 {
		return fmt.Errorf("unexpected WAITAOF reply %v", reply)
	}
	return nil
}
