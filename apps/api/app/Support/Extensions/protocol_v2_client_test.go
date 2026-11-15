package extensionsruntime

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/hashicorp/go-plugin"

	pluginbootstrap "github.com/keeptrue/sforum/apps/api/app/Support/PluginBootstrap"
)

// 宿主 env 里的哨兵键不能进插件进程（EX2-01 P1-2）。
//
// go-plugin 默认会在 Start 时把 os.Environ() 整段 append 到 cmd.Env，那会绕过
// buildPluginProcessEnv 的白名单，把 DATABASE_URL、会话签名键、SMTP 凭据一起交给
// 插件。`SkipHostEnv: true` 把这条旁路关掉；白名单自己带的基础变量（PATH/HOME/
// TMPDIR/LANG/TZ/APP_ENV/SFORUM_SETTING_*）仍然在。
//
// 断言直接落在「go-plugin 真实 append 之后」的 cmd.Env 上：用一个立刻退出的子进程
// 跑完 Start（握手失败是预期的），于是这里看到的就是插件实际收到的环境。
func TestPluginClientConfigSkipsHostEnvironment(t *testing.T) {
	t.Setenv("SFORUM_TEST_HOST_SECRET", "leak")
	t.Setenv("DATABASE_URL", "postgres://host-secret")
	t.Setenv("SFORUM_SETTING_ROOT_PATH", "/srv/forum-data")
	// 部署级非密钥配置必须存活：插件直连 TLS 时只能靠这两个变量找到自签 CA，
	// 代理变量同理（插件侧没有 CA / 代理设置项）。
	t.Setenv("SSL_CERT_FILE", "/etc/ssl/certs/sforum-test-ca.pem")
	t.Setenv("HTTPS_PROXY", "http://proxy.internal:3128")

	extension := runtimeExtension("env.whitelist.probe")
	extension.PackageDigest = strings.Repeat("c", 64)

	cmd := exec.Command("/bin/sh", "-c", "exit 0")
	cmd.Args = []string{"/bin/sh", "-c", "exit 0"}
	cmd.Env = buildPluginProcessEnv(os.Environ())

	starter := NewProtocolStarter(ProtocolStarterConfig{Trust: protocolDatabaseTrust{}})
	config, protocolName, err := starter.newPluginClientConfig(
		context.Background(), extension, cmd, "env-whitelist-instance",
	)
	if err != nil {
		t.Fatalf("build plugin client config: %v", err)
	}
	if protocolName != pluginbootstrap.ApplicationProtocolV2Name {
		t.Fatalf("protocol name = %q", protocolName)
	}
	client := plugin.NewClient(config)
	_, _ = client.Client() // 子进程立刻退出，握手失败是预期
	client.Kill()

	for _, leaked := range []string{"SFORUM_TEST_HOST_SECRET=", "DATABASE_URL="} {
		for _, entry := range cmd.Env {
			if strings.HasPrefix(entry, leaked) {
				t.Fatalf("host environment leaked into the plugin process: %s", entry)
			}
		}
	}
	// 白名单本身必须还在：否则「安全」是靠把插件饿死换来的。
	foundPath := false
	for _, entry := range cmd.Env {
		if strings.HasPrefix(entry, "PATH=") {
			foundPath = true
		}
	}
	if !foundPath {
		t.Fatalf("plugin environment lost PATH: %v", cmd.Env)
	}
	if !envEntryPresent(cmd.Env, "SFORUM_SETTING_ROOT_PATH=/srv/forum-data") {
		t.Fatalf("plugin settings did not survive: %v", cmd.Env)
	}
	// CA / 代理透传与 Landlock 的 CA 只读授权是一对：只留一边等于插件拿不到 CA。
	if !envEntryPresent(cmd.Env, "SSL_CERT_FILE=/etc/ssl/certs/sforum-test-ca.pem") {
		t.Fatalf("SSL_CERT_FILE did not survive the allowlist: %v", cmd.Env)
	}
	if !envEntryPresent(cmd.Env, "HTTPS_PROXY=http://proxy.internal:3128") {
		t.Fatalf("HTTPS_PROXY did not survive the allowlist: %v", cmd.Env)
	}
	// 结构断言放在行为断言之后：先让「到底泄了什么」说话，再守住配置位。
	if !config.SkipHostEnv {
		t.Fatal("ClientConfig.SkipHostEnv must be true: go-plugin would re-append os.Environ()")
	}
}

func envEntryPresent(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
