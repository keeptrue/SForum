package extensionsruntime

import (
	"context"
	"errors"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func TestValidatePluginRouteTargetAllowsLoopback(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1:43123",
		"http://localhost:8080/hooks",
		"https://[::1]:9443",
	} {
		if err := validatePluginRouteTarget(raw); err != nil {
			t.Fatalf("expected %q allowed, got %v", raw, err)
		}
	}
}

func TestIsPluginRouteTargetNone(t *testing.T) {
	for _, raw := range []string{"", "  ", "disabled", "DISABLED", "none", "None"} {
		if !isPluginRouteTargetNone(raw) {
			t.Fatalf("expected %q treated as no-route target", raw)
		}
	}
	for _, raw := range []string{"http://127.0.0.1:1", "disabled-http", "not-none"} {
		if isPluginRouteTargetNone(raw) {
			t.Fatalf("expected %q not treated as no-route target", raw)
		}
	}
}

func TestValidatePluginRouteTargetRejectsSSRF(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"http://169.254.169.254/latest/meta-data/",
		"http://10.0.0.5:80/",
		"http://192.168.1.1/",
		"http://user:pass@127.0.0.1:1/",
		"ftp://127.0.0.1/",
		"disabled", // 无路由哨兵不走 validate；若误传入必须拒绝
	} {
		if err := validatePluginRouteTarget(raw); err == nil {
			t.Fatalf("expected %q rejected", raw)
		}
	}
}

func TestBuildPluginProcessEnvOmitsHostSecrets(t *testing.T) {
	env := buildPluginProcessEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/sforum",
		"GODEBUG=http2debug=2",
		"DATABASE_URL=postgres://secret",
		"SESSION_HASH_SECRET=super-secret",
		"SFORUM_SETTING_HOST=smtp.example.com",
		"LANG=C.UTF-8",
		"RANDOM_JUNK=1",
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/sforum", "GODEBUG=disablethp=1", "GOGC=50", "GOMEMLIMIT=96MiB", "SFORUM_SETTING_HOST=smtp.example.com", "LANG=C.UTF-8"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected env to contain %q, got %v", want, env)
		}
	}
	for _, deny := range []string{"GODEBUG=http2debug=2", "DATABASE_URL=", "SESSION_HASH_SECRET=", "RANDOM_JUNK="} {
		if strings.Contains(joined, deny) {
			t.Fatalf("env must not contain host secret/junk %q, got %v", deny, env)
		}
	}
}

// 零配置生效：宿主不设 SFORUM_PLUGIN_GOMEMLIMIT 时注入固定默认值。
func TestBuildPluginProcessEnvDefaultsMemoryLimitWithoutOverride(t *testing.T) {
	env := buildPluginProcessEnv([]string{"PATH=/usr/bin", "GOMEMLIMIT=999MiB"})
	joined := strings.Join(env, "\n")
	// 宿主自身的 GOMEMLIMIT 不透传——只有覆盖键 SFORUM_PLUGIN_GOMEMLIMIT 生效。
	if strings.Contains(joined, "GOMEMLIMIT=999MiB") {
		t.Fatalf("host GOMEMLIMIT must not pass through, got %v", env)
	}
	if !strings.Contains(joined, "GOMEMLIMIT=96MiB") {
		t.Fatalf("expected default GOMEMLIMIT=96MiB, got %v", env)
	}
	if !strings.Contains(joined, "GOGC=50") {
		t.Fatalf("expected GOGC=50, got %v", env)
	}
}

// 可选覆盖：宿主环境带 SFORUM_PLUGIN_GOMEMLIMIT 时覆盖默认值；该键本身不得原样透传。
func TestBuildPluginProcessEnvHonorsGOMEMLIMITOverride(t *testing.T) {
	env := buildPluginProcessEnv([]string{
		"PATH=/usr/bin",
		"SFORUM_PLUGIN_GOMEMLIMIT=128MiB",
	})
	joined := strings.Join(env, "\n")
	if strings.Count(joined, "GOMEMLIMIT=") != 1 {
		t.Fatalf("expected exactly one GOMEMLIMIT entry, got %v", env)
	}
	if !strings.Contains(joined, "GOMEMLIMIT=128MiB") {
		t.Fatalf("expected override GOMEMLIMIT=128MiB, got %v", env)
	}
	// 空值覆盖视为未设置，回落默认。
	empty := buildPluginProcessEnv([]string{"PATH=/usr/bin", "SFORUM_PLUGIN_GOMEMLIMIT="})
	if !strings.Contains(strings.Join(empty, "\n"), "GOMEMLIMIT=96MiB") {
		t.Fatalf("empty override must fall back to default, got %v", empty)
	}
}

// T8C：production 宿主不得向插件透传 fake-GitHub 端点覆盖。
func TestBuildPluginProcessEnvStripsGitHubEndpointOverridesInProduction(t *testing.T) {
	env := buildPluginProcessEnv([]string{
		"APP_ENV=production",
		"PATH=/usr/bin",
		"SFORUM_AUTH_GITHUB_AUTH_URL=http://127.0.0.1:9/oauth/authorize",
		"SFORUM_AUTH_GITHUB_TOKEN_URL=http://127.0.0.1:9/oauth/token",
		"SFORUM_AUTH_GITHUB_API_URL=http://127.0.0.1:9/api",
		"SFORUM_SETTING_CLIENT_ID=cid",
		"SFORUM_SETTING_CLIENT_SECRET=csecret",
	})
	joined := strings.Join(env, "\n")
	for _, deny := range []string{
		"SFORUM_AUTH_GITHUB_AUTH_URL=",
		"SFORUM_AUTH_GITHUB_TOKEN_URL=",
		"SFORUM_AUTH_GITHUB_API_URL=",
	} {
		if strings.Contains(joined, deny) {
			t.Fatalf("production must strip %q, got %v", deny, env)
		}
	}
	for _, want := range []string{"APP_ENV=production", "PATH=/usr/bin", "SFORUM_SETTING_CLIENT_ID=cid"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("expected %q retained, got %v", want, env)
		}
	}
}

// T8C：非 production 仍允许注入 fake-GitHub 端点（本地/E2E）。
func TestBuildPluginProcessEnvAllowsGitHubEndpointOverridesOutsideProduction(t *testing.T) {
	for _, appEnv := range []string{"development", "test", "testing", ""} {
		host := []string{
			"PATH=/usr/bin",
			"SFORUM_AUTH_GITHUB_AUTH_URL=http://127.0.0.1:9/oauth/authorize",
			"SFORUM_AUTH_GITHUB_TOKEN_URL=http://127.0.0.1:9/oauth/token",
			"SFORUM_AUTH_GITHUB_API_URL=http://127.0.0.1:9/api",
		}
		if appEnv != "" {
			host = append(host, "APP_ENV="+appEnv)
		}
		env := buildPluginProcessEnv(host)
		joined := strings.Join(env, "\n")
		for _, want := range []string{
			"SFORUM_AUTH_GITHUB_AUTH_URL=http://127.0.0.1:9/oauth/authorize",
			"SFORUM_AUTH_GITHUB_TOKEN_URL=http://127.0.0.1:9/oauth/token",
			"SFORUM_AUTH_GITHUB_API_URL=http://127.0.0.1:9/api",
		} {
			if !strings.Contains(joined, want) {
				t.Fatalf("APP_ENV=%q must allow %q, got %v", appEnv, want, env)
			}
		}
	}
}

func TestProtocolStarterRejectsUnsupportedRPC(t *testing.T) {
	starter := NewProtocolStarter(ProtocolStarterConfig{})
	extension := runtimeExtension("bad.protocol")
	extension.Manifest.Backend.RPC = "custom"
	_, err := starter.Start(context.Background(), extension)
	if err == nil {
		t.Fatal("expected unsupported protocol error")
	}
}

func TestProtocolStarterRequiresBackendEntry(t *testing.T) {
	starter := NewProtocolStarter(ProtocolStarterConfig{})
	extension := runtimeExtension("missing.entry")
	extension.Manifest.Backend.Entry = ""
	_, err := starter.Start(context.Background(), extension)
	if err == nil {
		t.Fatal("expected missing entry error")
	}
}

func TestProtocolStarterRejectsLegacyManifestAndProtocolVersions(t *testing.T) {
	starter := NewProtocolStarter(ProtocolStarterConfig{})
	for _, test := range []struct {
		name            string
		manifestVersion int
		protocolVersion int
	}{
		{name: "manifest v2", manifestVersion: 2, protocolVersion: 2},
		{name: "unsupported protocol 1", manifestVersion: 3, protocolVersion: 1},
		{name: "missing protocol", manifestVersion: 3, protocolVersion: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			extension := runtimeExtension("legacy.protocol")
			extension.Manifest.ManifestVersion = test.manifestVersion
			extension.Manifest.Backend.ProtocolVersion = test.protocolVersion
			if _, err := starter.Start(context.Background(), extension); !errors.Is(err, ErrUnsupportedProtocol) {
				t.Fatalf("Start error = %v, want ErrUnsupportedProtocol", err)
			}
		})
	}
}

var _ Starter = (*ProtocolStarter)(nil)
var _ = extensions.TypePlugin
