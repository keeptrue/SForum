package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

	cache "github.com/keeptrue/sforum/apps/api/app/Support/Cache"
	cachepolicy "github.com/keeptrue/sforum/apps/api/app/Support/CachePolicy"
	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	installationidentity "github.com/keeptrue/sforum/apps/api/app/Support/InstallationIdentity"
)

type productionHostCache struct {
	Registry  *cacheregistry.Registry
	Backend   *hostapi.HostRedisCacheBackend
	Service   *hostapi.HostCacheService
	Protocol  *hostapi.ProtocolV2CacheServiceServer
	Inspector *hostapi.HostCacheInspector
	// Policy 是 Host 缓存策略运行时（隔离键规划 / tag 失效 / inspector）。
	// tag 失效的权威是同一个 Redis（P3-11，见
	// knowledge/decisions/2026-11-17-cache-policy-tag-generation.md）。
	Policy *cachepolicy.Service
}

// bindProductionHostCache assembles one process-wide Cache Registry and binds
// it before the first plugin broker freezes the Gateway service set.
func bindProductionHostCache(
	installationID string,
	logger *slog.Logger,
	client *redis.Client,
	registry *cacheregistry.Registry,
	admission hostapi.ServiceProviderAdmission,
	gateway *hostapi.Gateway,
) (*productionHostCache, error) {
	if client == nil || registry == nil || admission == nil || gateway == nil {
		return nil, fmt.Errorf("bootstrap: production Host Cache dependency unavailable")
	}
	if !installationidentity.Valid(installationID) {
		return nil, fmt.Errorf("bootstrap: durable Host Cache installation identity is invalid")
	}
	backend, err := hostapi.NewHostRedisCacheBackend(client)
	if err != nil {
		return nil, fmt.Errorf("create Host Redis cache backend: %w", err)
	}
	inspector := hostapi.NewHostCacheInspector(0)
	service, err := hostapi.NewHostCacheService(
		registry,
		backend,
		// 插件 provider 的远程 Cache 协议尚未冻结；显式选择必须 fail closed。
		nil,
		hostapi.WithHostCacheInstallationID(installationID),
		hostapi.WithHostCacheRuntimeAdmission(admission),
		hostapi.WithHostCacheTraceSink(hostapi.NewMultiHostCacheTraceSink(
			inspector,
			hostapi.NewSlogHostCacheTraceSink(logger),
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("create Host Cache service: %w", err)
	}
	protocol, err := hostapi.NewProtocolV2CacheServiceServer(service)
	if err != nil {
		return nil, fmt.Errorf("create Protocol V2 Cache service: %w", err)
	}
	if err := gateway.BindProtocolV2CacheService(protocol); err != nil {
		return nil, fmt.Errorf("bind Protocol V2 Cache service: %w", err)
	}
	// 缓存策略运行时与 Host Cache 共用同一个 Redis 连接：tag generation 必须与
	// 条目落在同一权威上，否则失效与读取会看到两个不同的世界。
	policy, err := cachepolicy.New(registry, cache.NewRedisCache(client), cachepolicy.ProviderRedis)
	if err != nil {
		return nil, fmt.Errorf("create Host cache policy: %w", err)
	}
	return &productionHostCache{
		Registry: registry, Backend: backend, Service: service,
		Protocol: protocol, Inspector: inspector, Policy: policy,
	}, nil
}
