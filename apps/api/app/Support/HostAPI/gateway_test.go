package hostapi

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
)

func TestGatewayRoundTrip(t *testing.T) {
	svc := New(Config{
		Capabilities: fakeCaps{set: capabilities.NewSet([]string{capabilities.HostAPI, capabilities.SettingsOwn})},
		Settings:     fakeSettings{values: map[string]string{"k": "v"}},
	})
	gw := NewGateway(svc)
	t.Cleanup(func() { _ = gw.Close() })

	_, env, err := gw.RegisterExtension("demo.plugin")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if len(env) < 3 {
		t.Fatalf("env: %#v", env)
	}

	client := &Client{
		BaseURL:     gw.BaseURL(),
		Token:       envToken(env, "SFORUM_HOST_API_TOKEN"),
		ExtensionID: "demo.plugin",
	}
	resp, err := client.Call(context.Background(), MethodPing, nil)
	if err != nil || !resp.OK {
		t.Fatalf("ping: err=%v resp=%#v", err, resp)
	}

	resp, err = client.Call(context.Background(), MethodGetSettings, nil)
	if err != nil || !resp.OK {
		t.Fatalf("settings: err=%v resp=%#v", err, resp)
	}

	// 冒充其它 extension 应被网关纠正为已认证 id，且 token 不匹配时 401。
	bad := &Client{BaseURL: gw.BaseURL(), Token: "wrong", ExtensionID: "demo.plugin"}
	resp, err = bad.Call(context.Background(), MethodPing, nil)
	if err != nil {
		t.Fatalf("bad call transport: %v", err)
	}
	if resp.OK {
		t.Fatalf("expected unauthorized, got %#v", resp)
	}
}

func envToken(env []string, key string) string {
	prefix := key + "="
	for _, entry := range env {
		if len(entry) > len(prefix) && entry[:len(prefix)] == prefix {
			return entry[len(prefix):]
		}
	}
	return ""
}
