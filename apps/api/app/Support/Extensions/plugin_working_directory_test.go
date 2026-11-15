package extensionsruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 这组用例守的是「cwd 钉在制品目录」这条边界。它不在 protocol.go 里，而是在
// preparePluginCommand 调用的 helper 上，所以直接用 fixture 断言返回值。

func workingDirectoryFixture(t *testing.T) (extensions.Extension, string) {
	t.Helper()
	packageRoot := filepath.Join(t.TempDir(), "working.dir", "1.0.0")
	backendDir := filepath.Join(packageRoot, "backend")
	if err := os.MkdirAll(backendDir, 0o755); err != nil {
		t.Fatal(err)
	}
	entry := filepath.Join(backendDir, "plugin")
	if err := os.WriteFile(entry, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	extension := runtimeExtension("working.dir")
	extension.Source = extensions.SourceUploaded
	extension.Version = "1.0.0"
	extension.PackageDigest = "c0ffee"
	extension.PackagePath = packageRoot
	return extension, entry
}

// allow：入口在包内容根之内时，cwd 就是后端入口所在目录（不再继承宿主 cwd）。
func TestPluginWorkingDirectoryPinsArtifactDirectory(t *testing.T) {
	extension, entry := workingDirectoryFixture(t)
	dir, err := pluginWorkingDirectory(extension, entry)
	if err != nil {
		t.Fatalf("artifact directory was rejected: %v", err)
	}
	if want := filepath.Dir(entry); dir != want {
		t.Fatalf("plugin cwd = %q, want the artifact directory %q", dir, want)
	}
}

// deny：入口落在包内容根之外时拒绝启动（不能把 cwd 设到包外的相邻目录）。
func TestPluginWorkingDirectoryRejectsEntryOutsidePackage(t *testing.T) {
	extension, _ := workingDirectoryFixture(t)
	outside := filepath.Join(t.TempDir(), "elsewhere", "plugin")
	if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(outside, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := pluginWorkingDirectory(extension, outside); !errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("entry outside the package root was accepted: %v", err)
	}
}

// deny：宿主工作树落进插件 cwd 时拒绝启动。把 cwd 切到制品目录内部即可复现
// 「插件的相对路径写入会落进宿主工作树」的那种配置。
func TestPluginWorkingDirectoryRejectsHostWorkingTree(t *testing.T) {
	extension, entry := workingDirectoryFixture(t)
	t.Chdir(filepath.Dir(entry))
	if _, err := pluginWorkingDirectory(extension, entry); !errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("host working tree inside the plugin cwd was accepted: %v", err)
	}
}
