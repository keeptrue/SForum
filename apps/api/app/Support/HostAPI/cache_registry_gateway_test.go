package hostapi

import (
	"context"
	"net"
	"sync"
	"testing"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestGatewayBindsAndFreezesProtocolV2CacheService(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "gateway.cache", "public", "")
	cache, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2CacheService(nil); err == nil {
		t.Fatal("nil cache service was accepted")
	}
	if err := gateway.BindProtocolV2CacheService(cache); err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2CacheService(cache); err == nil {
		t.Fatal("cache service rebound before broker registration")
	}

	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, ok := server.GetServiceInfo()["sforum.host.v2.CacheService"]; !ok {
		t.Fatal("bound CacheService was not registered")
	}
	if gateway.cache != cache || !gateway.protocolV2CacheFrozen {
		t.Fatalf("cache snapshot was not frozen: cache=%p frozen=%t", gateway.cache, gateway.protocolV2CacheFrozen)
	}
	if err := gateway.BindProtocolV2CacheService(cache); err == nil {
		t.Fatal("cache service rebound after broker registration")
	}
}

func TestGatewayProtocolV2CacheServiceRoundTrip(t *testing.T) {
	fixture := newHostCacheTestFixture(t, "gateway.roundtrip.cache", "public", "")
	cache, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	gateway := NewGateway(New(Config{}))
	if err := gateway.BindProtocolV2CacheService(cache); err != nil {
		t.Fatal(err)
	}

	listener := bufconn.Listen(1 << 20)
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})
	connection, err := grpc.NewClient(
		"passthrough:///host-cache-gateway-test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	response, err := hostv2.NewCacheServiceClient(connection).Get(context.Background(), &hostv2.CacheGetRequest{})
	if err != nil {
		t.Fatalf("bound CacheService was unreachable: %v", err)
	}
	if response.GetError().GetReason() != "host.cache_runtime_stale" {
		t.Fatalf("request did not reach the cache server: %#v", response)
	}
}

func TestGatewayCacheBindAndRegisterAreRaceSafe(t *testing.T) {
	for range 32 {
		fixture := newHostCacheTestFixture(t, "gateway.race.cache", "public", "")
		cache, err := NewProtocolV2CacheServiceServer(fixture.service)
		if err != nil {
			t.Fatal(err)
		}
		gateway := NewGateway(New(Config{}))
		server := grpc.NewServer()
		start := make(chan struct{})
		var bindErr error
		var wait sync.WaitGroup
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			bindErr = gateway.BindProtocolV2CacheService(cache)
		}()
		go func() {
			defer wait.Done()
			<-start
			gateway.RegisterProtocolV2(server)
		}()
		close(start)
		wait.Wait()

		_, registered := server.GetServiceInfo()["sforum.host.v2.CacheService"]
		if (bindErr == nil) != registered {
			t.Fatalf("bind error=%v registered=%t", bindErr, registered)
		}
		if !gateway.protocolV2CacheFrozen {
			t.Fatal("broker registration did not freeze cache binding")
		}
		server.Stop()
	}
}

func TestGatewayWithoutCacheBindingFailsClosed(t *testing.T) {
	gateway := NewGateway(New(Config{}))
	server := grpc.NewServer()
	gateway.RegisterProtocolV2(server)
	if _, registered := server.GetServiceInfo()["sforum.host.v2.CacheService"]; registered {
		t.Fatal("unbound CacheService was registered")
	}
	fixture := newHostCacheTestFixture(t, "gateway.unbound.cache", "public", "")
	cache, err := NewProtocolV2CacheServiceServer(fixture.service)
	if err != nil {
		t.Fatal(err)
	}
	if err := gateway.BindProtocolV2CacheService(cache); err == nil {
		t.Fatal("cache binding reopened after an unbound broker snapshot")
	}
}
