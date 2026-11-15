package extensionsruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// execSentinelExtension 造一个「已安装」的 v2 插件制品：launcher 脚本被真正执行时
// 会写下 sentinel 文件，因此测试可以直接观察 exec 有没有发生。
func execSentinelExtension(t *testing.T, declaredDigest string) (extensions.Extension, string) {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "sentinel.plugin", "1.0.0")
	backendDir := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(t.TempDir(), "executed")
	launcher := "#!/bin/sh\ntouch " + shellQuote(sentinel) + "\nexit 0\n"
	if err := os.WriteFile(filepath.Join(backendDir, "plugin"), []byte(launcher), 0o755); err != nil {
		t.Fatal(err)
	}
	extension := runtimeExtension("sentinel.plugin")
	extension.Source = extensions.SourceUploaded
	extension.Version = "1.0.0"
	extension.PackageDigest = strings.Repeat("b", 64)
	extension.PackagePath = packageRoot
	extension.Manifest.Backend.Digest = declaredDigest
	return extension, sentinel
}

// denied：exec 前复核不通过时，进程绝不能被真正启动（EX2-04b / EX2-01a）。
func TestProtocolStartRefusesUntrustedExecutableBeforeExec(t *testing.T) {
	extension, sentinel := execSentinelExtension(t, strings.Repeat("f", 64))
	starter := NewProtocolStarter(ProtocolStarterConfig{Trust: protocolDatabaseTrust{}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := starter.Start(ctx, extension)
	if err == nil {
		t.Fatal("plugin with a mismatched declared digest was started")
	}
	if !errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("expected the pre-exec trust gate to reject, got %v", err)
	}
	if !errors.Is(err, extensions.ErrFrontendPackageChanged) {
		t.Fatalf("rejection should keep the digest-cause sentinel, got %v", err)
	}
	if _, statErr := os.Stat(sentinel); statErr == nil {
		t.Fatal("plugin launcher ran despite the failed pre-exec verification")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unexpected sentinel stat error: %v", statErr)
	}
}

// allowed：声明摘要与内容一致时，复核放行，exec 正常发生（随后由 go-plugin 握手
// 判定脚本不是合法插件而失败——这是预期的下一层失败，不再是信任门拒绝）。
func TestProtocolStartExecutesVerifiedExecutable(t *testing.T) {
	extension, sentinel := execSentinelExtension(t, "")
	// 声明摘要按真实内容计算，等价于构建期 digest --write 写回的结果。
	sum, err := extensions.VerifyRuntimeExecutableDigest(extension)
	if err != nil {
		t.Fatalf("fixture digest: %v", err)
	}
	extension.Manifest.Backend.Digest = sum

	starter := NewProtocolStarter(ProtocolStarterConfig{Trust: protocolDatabaseTrust{}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if _, err := starter.Start(ctx, extension); err == nil {
		t.Fatal("a launcher that never speaks go-plugin unexpectedly started")
	} else if errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("verified executable was rejected by the trust gate: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, statErr := os.Stat(sentinel); statErr == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("verified plugin launcher never ran")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
