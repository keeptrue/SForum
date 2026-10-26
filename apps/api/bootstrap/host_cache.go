package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/redis/go-redis/v9"

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
	return &productionHostCache{
		Registry: registry, Backend: backend, Service: service,
		Protocol: protocol, Inspector: inspector,
	}, nil
}
