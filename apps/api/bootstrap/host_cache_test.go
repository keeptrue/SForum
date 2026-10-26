package bootstrap

import (
	"context"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"

	cacheregistry "github.com/keeptrue/sforum/apps/api/app/Support/CacheRegistry"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

var validHostCacheInstallationID = strings.Repeat("a", 64)

type hostCacheBootstrapAdmission struct{}

func (hostCacheBootstrapAdmission) AcquireServiceProvider(
	context.Context,
	hostapi.ServiceProviderIdentity,
) (hostapi.ServiceProviderAdmissionLease, error) {
	return nil, hostapi.ErrServiceProviderStale
}

func TestProductionHostCacheBindsOneSharedRegistry(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	registry := cacheregistry.New()
	gateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = gateway.Close() })

	runtime, err := bindProductionHostCache(
		validHostCacheInstallationID,
		nil, client, registry, hostCacheBootstrapAdmission{}, gateway,
	)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.Registry != registry || runtime.Backend == nil || runtime.Service == nil ||
		runtime.Protocol == nil || runtime.Inspector == nil {
		t.Fatalf("production Host Cache = %#v", runtime)
	}
	if err := gateway.BindProtocolV2CacheService(runtime.Protocol); err == nil {
		t.Fatal("Gateway accepted a second Cache service binding")
	}
}

func TestProductionHostCacheRejectsMissingDependencies(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	registry := cacheregistry.New()
	gateway := hostapi.NewGateway(nil)
	t.Cleanup(func() { _ = gateway.Close() })

	for name, run := range map[string]func() error{
		"redis": func() error {
			_, err := bindProductionHostCache(validHostCacheInstallationID, nil, nil, registry, hostCacheBootstrapAdmission{}, gateway)
			return err
		},
		"registry": func() error {
			_, err := bindProductionHostCache(validHostCacheInstallationID, nil, client, nil, hostCacheBootstrapAdmission{}, gateway)
			return err
		},
		"admission": func() error {
			_, err := bindProductionHostCache(validHostCacheInstallationID, nil, client, registry, nil, gateway)
			return err
		},
		"gateway": func() error {
			_, err := bindProductionHostCache(validHostCacheInstallationID, nil, client, registry, hostCacheBootstrapAdmission{}, nil)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := run(); err == nil {
				t.Fatal("missing dependency was accepted")
			}
		})
	}
}

func TestProductionHostCacheRejectsInvalidDurableIdentity(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })
	for _, installationID := range []string{"", "postgres://operator:secret@db/sforum", strings.Repeat("A", 64)} {
		registry := cacheregistry.New()
		gateway := hostapi.NewGateway(nil)
		_, err := bindProductionHostCache(
			installationID, nil, client, registry, hostCacheBootstrapAdmission{}, gateway,
		)
		_ = gateway.Close()
		if err == nil {
			t.Fatalf("invalid installation identity accepted: %q", installationID)
		}
	}
}
