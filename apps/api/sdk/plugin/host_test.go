package plugin

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

// fakeCaps 最小能力源，供 Host API 网关集成测试。
type fakeCaps struct {
	set capabilities.Set
}

func (f fakeCaps) CapabilitiesFor(context.Context, string) (capabilities.Set, error) {
	return f.set, nil
}

func (f fakeCaps) DeclaredJobKinds(context.Context, string) ([]string, error) {
	return nil, nil
}

func TestHostPingViaGateway(t *testing.T) {
	svc := hostapi.New(hostapi.Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI})},
	})
	gw := hostapi.NewGateway(svc)
	t.Cleanup(func() { _ = gw.Close() })

	_, env, err := gw.RegisterExtension("sdk.fixture")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	token := envValue(env, "SFORUM_HOST_API_TOKEN")
	client := &Host{
		BaseURL:     gw.BaseURL(),
		Token:       token,
		ExtensionID: "sdk.fixture",
	}
	resp, err := Ping(context.Background(), client)
	if err != nil || !resp.OK {
		t.Fatalf("Ping: err=%v resp=%#v", err, resp)
	}
	if resp.Data["version"] != HostAPIVersion {
		t.Fatalf("version=%v", resp.Data["version"])
	}
}

func envValue(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if len(entry) > len(prefix) && entry[:len(prefix)] == prefix {
			return entry[len(prefix):]
		}
	}
	return ""
}
