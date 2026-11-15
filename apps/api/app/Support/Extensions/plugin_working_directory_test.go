package extensionsruntime

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

// 这组用例守的是「cwd 钉在制品目录 + exec 走绝对路径」这条边界。它不在
// protocol.go 里，而是在 preparePluginCommand 调用的 helper 上，所以直接用
// fixture 断言返回值。

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
	paths, err := resolvePluginCommandPaths(extension, entry)
	if err != nil {
		t.Fatalf("artifact directory was rejected: %v", err)
	}
	if want := filepath.Dir(entry); paths.WorkDir != want {
		t.Fatalf("plugin cwd = %q, want the artifact directory %q", paths.WorkDir, want)
	}
	// cwd 与 exec 目标必须来自同一次解析：entry 已是绝对路径，解析后原样返回。
	if paths.Entry != entry {
		t.Fatalf("plugin exec path = %q, want the resolved entry %q", paths.Entry, entry)
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
	if _, err := resolvePluginCommandPaths(extension, outside); !errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("entry outside the package root was accepted: %v", err)
	}
}

// deny：宿主工作树落进插件 cwd 时拒绝启动。把 cwd 切到制品目录内部即可复现
// 「插件的相对路径写入会落进宿主工作树」的那种配置。
func TestPluginWorkingDirectoryRejectsHostWorkingTree(t *testing.T) {
	extension, entry := workingDirectoryFixture(t)
	t.Chdir(filepath.Dir(entry))
	if _, err := resolvePluginCommandPaths(extension, entry); !errors.Is(err, ErrPluginExecutableUntrusted) {
		t.Fatalf("host working tree inside the plugin cwd was accepted: %v", err)
	}
}

// relativePackagePath 把绝对制品目录折算成 dev 环境里的那种相对 PackagePath
// （相对宿主 cwd，形如 ../../storage/extensions/<id>/<version>/<digest>）。
func relativePackagePath(t *testing.T, packageRoot string) string {
	t.Helper()
	hostWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("host working directory: %v", err)
	}
	relative, err := filepath.Rel(hostWorkingDirectory, packageRoot)
	if err != nil {
		t.Fatalf("relativize package path: %v", err)
	}
	if filepath.IsAbs(relative) {
		t.Fatalf("relative package path %q is still absolute", relative)
	}
	return relative
}

// 回归（EX2-01 引入的可用性故障）：dev 环境的 PackagePath 是相对路径，
// installedFilePath 因此给出相对入口。exec 的路径必须是绝对路径——execve 用相对
// 路径时会以子进程 cwd（这里就是被钉成绝对制品目录的 cmd.Dir）为基准解析，两者
// 叠加就是 fork/exec ../../storage/.../backend/plugin: no such file or directory。
//
// 这条用例只走 preparePluginCommand，不依赖 helper 的内部签名：命令组装完成后
// 断言 exec 路径绝对、cwd 仍是制品目录、两者指向同一个文件。
func TestPreparePluginCommandExecsAbsoluteEntryForRelativePackage(t *testing.T) {
	extension, entry := workingDirectoryFixture(t)
	extension.PackagePath = relativePackagePath(t, extension.PackagePath)

	entryPath, ok := extensions.InstalledFilePathForRuntime(extension, extension.Manifest.Backend.Entry)
	if !ok {
		t.Fatal("resolve backend entry from a relative package path")
	}
	// 前置条件：入口路径像 dev 一样是相对的，否则这条用例守不住任何东西。
	if filepath.IsAbs(entryPath) {
		t.Fatalf("fixture precondition failed: %q should be relative to the host cwd", entryPath)
	}

	cmd, err := preparePluginCommand(extension, entryPath)
	if err != nil {
		t.Fatalf("prepare plugin command: %v", err)
	}
	if !filepath.IsAbs(cmd.Path) {
		t.Fatalf("plugin exec path %q is relative; the kernel resolves it against cmd.Dir %q", cmd.Path, cmd.Dir)
	}
	if want := filepath.Dir(entry); cmd.Dir != want {
		t.Fatalf("plugin cwd = %q, want the artifact directory %q", cmd.Dir, want)
	}
	execInfo, err := os.Stat(cmd.Path)
	if err != nil {
		t.Fatalf("stat exec path %q: %v", cmd.Path, err)
	}
	entryInfo, err := os.Stat(entry)
	if err != nil {
		t.Fatalf("stat backend entry %q: %v", entry, err)
	}
	if !os.SameFile(execInfo, entryInfo) {
		t.Fatalf("exec path %q and backend entry %q are not the same file", cmd.Path, entry)
	}
}
