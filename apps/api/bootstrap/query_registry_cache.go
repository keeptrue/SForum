package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/redis/go-redis/v9"

	queryregistry "github.com/keeptrue/sforum/apps/api/app/Support/QueryRegistry"
	"github.com/keeptrue/sforum/apps/api/config"
)

type productionQueryResultCacheRuntime struct {
	client *redis.Client
	cache  *queryregistry.RedisQueryResultCache

	closeOnce sync.Once
}

type productionQueryResultCacheStarter func(
	context.Context,
	config.Config,
	string,
) (*productionQueryResultCacheRuntime, error)

type ownedProductionQueryInvalidator interface {
	queryregistry.SemanticCacheInvalidator
	Close(*slog.Logger)
}

type productionQueryInvalidatorFactory func(context.Context) (ownedProductionQueryInvalidator, error)

// recoveringProductionQueryInvalidator serializes invalidations and replaces a
// terminally failed Redis authority instead of retrying its latched cache object.
type recoveringProductionQueryInvalidator struct {
	mu      sync.Mutex
	factory productionQueryInvalidatorFactory
	current ownedProductionQueryInvalidator
	logger  *slog.Logger
	closed  bool
}

// productionQueryInvalidationRuntime owns only the worker-side Redis authority.
// It is deliberately a different type from the API execution cache runtime so
// embedded workers cannot accidentally share and poison the API cache client.
type productionQueryInvalidationRuntime struct {
	invalidator *recoveringProductionQueryInvalidator
	closeOnce   sync.Once
}

func startProductionQueryResultCache(
	ctx context.Context,
	cfg config.Config,
	installationID string,
) (*productionQueryResultCacheRuntime, error) {
	if cfg.SafeMode {
		return &productionQueryResultCacheRuntime{}, nil
	}
	client := newProductionQueryResultCacheClient(cfg)
	cache, err := queryregistry.NewRedisQueryResultCache(client, installationID)
	if err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("construct Query result cache: %w", err)
	}
	if err := cache.Activate(ctx); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("activate Query result cache: %w", err)
	}
	return &productionQueryResultCacheRuntime{client: client, cache: cache}, nil
}

// loadOptionalProductionQueryResultCache keeps Query execution available when
// Redis cannot satisfy the cache authority contract. Invalid Host identity or
// canceled startup still fails boot because neither is a cache outage.
func loadOptionalProductionQueryResultCache(
	ctx context.Context,
	cfg config.Config,
	installationID string,
	logger *slog.Logger,
) (*productionQueryResultCacheRuntime, error) {
	return loadOptionalProductionQueryResultCacheWithStarter(
		ctx, cfg, installationID, logger, startProductionQueryResultCache,
	)
}

func loadOptionalProductionQueryResultCacheWithStarter(
	ctx context.Context,
	cfg config.Config,
	installationID string,
	logger *slog.Logger,
	starter productionQueryResultCacheStarter,
) (*productionQueryResultCacheRuntime, error) {
	if ctx == nil || starter == nil {
		return nil, queryregistry.ErrExecutionInvalid
	}
	if cause := context.Cause(ctx); cause != nil {
		return nil, productionQueryContextError(ctx)
	}
	runtime, err := starter(ctx, cfg, installationID)
	if cause := context.Cause(ctx); cause != nil {
		if runtime != nil {
			runtime.Close(logger)
		}
		return nil, productionQueryContextError(ctx)
	}
	if err == nil {
		if runtime == nil {
			return nil, queryregistry.ErrExecutionInvalid
		}
		return runtime, nil
	}
	if runtime != nil {
		runtime.Close(logger)
	}
	if errors.Is(err, queryregistry.ErrExecutionInvalid) {
		return nil, err
	}
	if logger != nil {
		logger.Warn("Query result cache disabled",
			"error_class", productionQueryCacheErrorClass(err))
	}
	return &productionQueryResultCacheRuntime{}, nil
}

func newProductionQueryResultCacheClient(cfg config.Config) *redis.Client {
	return redis.NewClient(&redis.Options{
		Addr:                  cfg.RedisAddr,
		Password:              cfg.RedisPassword,
		PoolSize:              cfg.RedisPoolSize,
		MinIdleConns:          cfg.RedisMinIdleConns,
		DialTimeout:           cfg.RedisDialTimeout,
		ReadTimeout:           cfg.RedisReadTimeout,
		WriteTimeout:          cfg.RedisWriteTimeout,
		ConnMaxIdleTime:       cfg.RedisConnMaxIdleTime,
		ConnMaxLifetime:       cfg.RedisConnMaxLifetime,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
	})
}

func newProductionQueryInvalidationRuntime(
	cfg config.Config,
	installationID string,
	logger *slog.Logger,
) *productionQueryInvalidationRuntime {
	if cfg.SafeMode {
		return &productionQueryInvalidationRuntime{}
	}
	return newProductionQueryInvalidationRuntimeWithFactory(func(ctx context.Context) (ownedProductionQueryInvalidator, error) {
		runtime, err := startProductionQueryResultCache(ctx, cfg, installationID)
		if err != nil {
			return nil, err
		}
		if runtime == nil || runtime.cache == nil {
			if runtime != nil {
				runtime.Close(logger)
			}
			return nil, queryregistry.ErrExecutionInvalid
		}
		return runtime, nil
	}, logger)
}

// Keep embedded and standalone construction as named ownership boundaries even
// while both use the same Redis policy. Their clients must never be handed off
// from the API execution cache or shared with each other.
func newEmbeddedWorkerQueryInvalidationRuntime(
	cfg config.Config,
	installationID string,
	logger *slog.Logger,
) *productionQueryInvalidationRuntime {
	return newProductionQueryInvalidationRuntime(cfg, installationID, logger)
}

func newStandaloneWorkerQueryInvalidationRuntime(
	cfg config.Config,
	installationID string,
	logger *slog.Logger,
) *productionQueryInvalidationRuntime {
	return newProductionQueryInvalidationRuntime(cfg, installationID, logger)
}

func newProductionQueryInvalidationRuntimeWithFactory(
	factory productionQueryInvalidatorFactory,
	logger *slog.Logger,
) *productionQueryInvalidationRuntime {
	if factory == nil {
		return &productionQueryInvalidationRuntime{}
	}
	return &productionQueryInvalidationRuntime{invalidator: &recoveringProductionQueryInvalidator{
		factory: factory,
		logger:  logger,
	}}
}

func (r *productionQueryResultCacheRuntime) Cache() queryregistry.QueryResultCache {
	if r == nil || r.cache == nil {
		return nil
	}
	return r.cache
}

func (r *productionQueryResultCacheRuntime) Invalidator() queryregistry.SemanticCacheInvalidator {
	if r == nil || r.cache == nil {
		return nil
	}
	return r
}

func (r *productionQueryResultCacheRuntime) InvalidateOwnerTags(
	ctx context.Context,
	owner string,
	tags []string,
) (uint64, error) {
	if r == nil || r.cache == nil {
		return 0, queryregistry.ErrExecutionInvalid
	}
	return r.cache.InvalidateOwnerTags(ctx, owner, tags)
}

func (r *productionQueryResultCacheRuntime) Close(logger *slog.Logger) {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.client == nil {
			return
		}
		if err := r.client.Close(); err != nil && logger != nil {
			logger.Warn("Query result cache Redis close failed", "error_class", "close")
		}
	})
}

// queryResultCacheStageHandoff 跨 NewAPI 装配阶段持有 Query result cache 的关闭所有权。
// Stage A（wireAPICoreStack）defer CloseUnlessHandedOff；成功时 HandOff 给 Stage B
// （finishAPIHTTP），Stage B 再 defer 并在 API 句柄接管后 HandOff，最终由 API.close 关闭。
// 若 Stage A 在 HandOff 前就 return，defer 会关掉 runtime，避免泄漏。
type queryResultCacheStageHandoff struct {
	runtime   *productionQueryResultCacheRuntime
	handedOff bool
	logger    *slog.Logger
}

func newQueryResultCacheStageHandoff(
	runtime *productionQueryResultCacheRuntime,
	logger *slog.Logger,
) *queryResultCacheStageHandoff {
	return &queryResultCacheStageHandoff{runtime: runtime, logger: logger}
}

// CloseUnlessHandedOff 仅在尚未移交所有权时关闭 runtime（供 defer 使用）。
func (h *queryResultCacheStageHandoff) CloseUnlessHandedOff() {
	if h == nil || h.handedOff {
		return
	}
	if h.runtime != nil {
		h.runtime.Close(h.logger)
	}
}

// HandOff 将所有权交给下一阶段或 API.close，并返回同一 runtime 引用。
func (h *queryResultCacheStageHandoff) HandOff() *productionQueryResultCacheRuntime {
	if h == nil {
		return nil
	}
	h.handedOff = true
	return h.runtime
}

// Runtime 返回当前 runtime（不改变所有权）。
func (h *queryResultCacheStageHandoff) Runtime() *productionQueryResultCacheRuntime {
	if h == nil {
		return nil
	}
	return h.runtime
}

func (r *productionQueryInvalidationRuntime) Invalidator() queryregistry.SemanticCacheInvalidator {
	if r == nil || r.invalidator == nil {
		return nil
	}
	return r.invalidator
}

func (r *productionQueryInvalidationRuntime) Close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		if r.invalidator != nil {
			r.invalidator.Close()
		}
	})
}

func (r *recoveringProductionQueryInvalidator) InvalidateOwnerTags(
	ctx context.Context,
	owner string,
	tags []string,
) (uint64, error) {
	if ctx == nil {
		return 0, queryregistry.ErrExecutionInvalid
	}
	r.mu.Lock()
	if cause := context.Cause(ctx); cause != nil {
		r.mu.Unlock()
		return 0, productionQueryContextError(ctx)
	}
	if r.closed {
		r.mu.Unlock()
		return 0, queryregistry.ErrCacheCapability
	}
	current := r.current
	if current == nil {
		if r.factory == nil {
			r.mu.Unlock()
			return 0, queryregistry.ErrExecutionInvalid
		}
		created, err := r.factory(ctx)
		if err != nil {
			if created != nil {
				created.Close(r.logger)
			}
			r.mu.Unlock()
			return 0, err
		}
		if created == nil {
			r.mu.Unlock()
			return 0, queryregistry.ErrExecutionInvalid
		}
		r.current = created
		current = created
	}
	rotated, err := current.InvalidateOwnerTags(ctx, owner, tags)
	if err == nil || errors.Is(err, queryregistry.ErrInvalid) {
		r.mu.Unlock()
		return rotated, err
	}
	// Every non-input Redis failure terminally latches the concrete cache. Detach
	// it before returning so the snoozed job gets a fresh authority next time.
	// Close while holding the serialization lock so a concurrent shutdown cannot
	// report completion before the terminal authority has actually been released.
	r.current = nil
	current.Close(r.logger)
	r.mu.Unlock()
	return 0, err
}

func (r *recoveringProductionQueryInvalidator) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	current := r.current
	r.current = nil
	r.mu.Unlock()
	if current != nil {
		current.Close(r.logger)
	}
}

func productionQueryCacheErrorClass(err error) string {
	switch {
	case errors.Is(err, queryregistry.ErrExecutionInvalid):
		return "runtime_invalid"
	case errors.Is(err, queryregistry.ErrCacheCapability):
		return "capability"
	case errors.Is(err, queryregistry.ErrCachePoisoned):
		return "poisoned"
	case errors.Is(err, queryregistry.ErrCacheDurability):
		return "durability"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "transport"
	}
}
