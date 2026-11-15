//go:build linux

package extensionsruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 校验通过之后、exec 之前替换后端入口，必须执行**已校验的 inode**（RV-53）。
//
// 这条守的是「校验与使用是同一个对象」：只按路径 exec 时，rename 换掉文件就会
// 运行未校验的内容；把 exec 绑定到已打开的句柄后，替换不再被采纳。
func TestPinnedExecutableRunsVerifiedInodeAfterPathSwap(t *testing.T) {
	extension, entry, verifiedSentinel, swappedSentinel := pinnedExecFixture(t)

	executable, _, err := extensions.OpenVerifiedRuntimeExecutable(extension)
	if err != nil {
		t.Fatalf("open verified executable: %v", err)
	}
	defer executable.Close()

	cmd, err := preparePluginCommand(extension, entry)
	if err != nil {
		t.Fatalf("prepare plugin command: %v", err)
	}
	if err := pinVerifiedExecutable(cmd, executable); err != nil {
		t.Fatalf("pin verified executable: %v", err)
	}

	// 校验已完成：把路径上的文件换成另一个 launcher（rename 换 inode）。
	swapped := filepath.Join(filepath.Dir(entry), "plugin.swapped")
	if err := os.WriteFile(swapped, []byte(launcherScript(swappedSentinel)), 0o755); err != nil {
		t.Fatalf("stage swapped launcher: %v", err)
	}
	if err := os.Rename(swapped, entry); err != nil {
		t.Fatalf("swap backend entry: %v", err)
	}

	if err := cmd.Start(); err != nil {
		t.Fatalf("start pinned plugin command: %v", err)
	}
	_ = cmd.Wait()

	if _, statErr := os.Stat(verifiedSentinel); statErr != nil {
		t.Fatalf("the verified inode never ran: %v", statErr)
	}
	if _, statErr := os.Stat(swappedSentinel); statErr == nil {
		t.Fatal("exec followed the swapped path: the pinned command ran the replacement launcher")
	} else if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unexpected swapped sentinel stat error: %v", statErr)
	}
	// 结构断言放在行为断言之后：先让「执行了哪份内容」说话。
	if cmd.Path != pinnedExecutablePath {
		t.Fatalf("plugin command still executes by path %q, want %q", cmd.Path, pinnedExecutablePath)
	}
	if len(cmd.ExtraFiles) != 1 {
		t.Fatalf("pinned command extra files = %d, want 1", len(cmd.ExtraFiles))
	}
}

func pinnedExecFixture(t *testing.T) (extensions.Extension, string, string, string) {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "pinned.plugin", "1.0.0")
	backendDir := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	verifiedSentinel := filepath.Join(t.TempDir(), "verified-ran")
	swappedSentinel := filepath.Join(t.TempDir(), "swapped-ran")
	entry := filepath.Join(backendDir, "plugin")
	if err := os.WriteFile(entry, []byte(launcherScript(verifiedSentinel)), 0o755); err != nil {
		t.Fatal(err)
	}
	extension := runtimeExtension("pinned.plugin")
	extension.Source = extensions.SourceUploaded
	extension.Version = "1.0.0"
	extension.PackageDigest = "b3f1c0d2e4a5b6c7d8e9f0a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1"
	extension.PackagePath = packageRoot
	extension.Manifest.Backend.Digest = ""
	sum, err := extensions.VerifyRuntimeExecutableDigest(extension)
	if err != nil {
		t.Fatalf("fixture digest: %v", err)
	}
	extension.Manifest.Backend.Digest = sum
	return extension, entry, verifiedSentinel, swappedSentinel
}

func launcherScript(sentinel string) string {
	return "#!/bin/sh\ntouch " + shellQuote(sentinel) + "\nexit 0\n"
}
