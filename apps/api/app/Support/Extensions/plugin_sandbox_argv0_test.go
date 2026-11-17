package extensionsruntime

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	processmemory "github.com/keeptrue/sforum/apps/api/app/Support/ProcessMemory"
)

// 这组用例守的是「插件进程能不能被宿主认回来」：argv[0] 不是展示字段，它是
// processmemory 采样器唯一的识别依据。摘掉绝对路径，ps 里就只剩 backend/plugin，
// 采样器把这批子进程全部丢掉，后台显示「插件占用 0 个进程」。
func TestPluginSandboxArgv0IsScannableArtifactPath(t *testing.T) {
	digest := strings.Repeat("a", 64)
	artifactDir := filepath.Join(
		"/repo", "storage", "extensions", "sforum-shortcodes", "1.3.10", digest, "backend",
	)
	extension := extensions.Extension{
		ID:   "sforum-shortcodes",
		Type: extensions.TypePlugin,
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "backend/plugin"},
		},
	}
	// preparePluginCommand 用 exec.Command(入口绝对路径) 构造，Args[0] 就是绝对路径。
	cmd := exec.Command(filepath.Join(artifactDir, "plugin"))
	cmd.Dir = artifactDir

	argv0 := pluginSandboxArgv0(extension, cmd)
	if !filepath.IsAbs(argv0) {
		t.Fatalf("argv0=%q is not an absolute artifact path", argv0)
	}
	if !processmemory.IsBackendPluginCommand(argv0) {
		t.Fatalf("argv0=%q is not recognisable as a backend plugin command", argv0)
	}
	extensionID, ok := processmemory.ExtensionIDFromPluginCommand(argv0)
	if !ok || extensionID != "sforum-shortcodes" {
		t.Fatalf("argv0=%q parsed extension id=%q ok=%t", argv0, extensionID, ok)
	}

	// 回归钉子：本 bug 的形态就是「清单相对入口被当成 argv[0]」，它本身不可识别。
	if processmemory.IsBackendPluginCommand("backend/plugin") {
		t.Fatal("manifest-relative entry must not be recognisable as a plugin command")
	}
}

// 没有 Args[0]（例如测试里手工拼的 Cmd）时用钉住的制品 cwd + 清单入口文件名还原。
func TestPluginSandboxArgv0FallsBackToArtifactWorkDir(t *testing.T) {
	digest := strings.Repeat("b", 64)
	artifactDir := filepath.Join(
		"/repo", "storage", "extensions", "sforum-shortcodes", "1.3.10", digest, "backend",
	)
	extension := extensions.Extension{
		ID:   "sforum-shortcodes",
		Type: extensions.TypePlugin,
		Manifest: extensions.Manifest{
			Backend: extensions.ManifestBackend{Entry: "backend/plugin"},
		},
	}

	argv0 := pluginSandboxArgv0(extension, &exec.Cmd{Dir: artifactDir})
	if want := filepath.Join(artifactDir, "plugin"); argv0 != want {
		t.Fatalf("argv0=%q want %q", argv0, want)
	}
	if !processmemory.IsBackendPluginCommand(argv0) {
		t.Fatalf("fallback argv0=%q is not recognisable", argv0)
	}

	// 连制品目录都没有时不伪造路径：退回可读 ID（该进程本来就无法被采样器认领）。
	if got := pluginSandboxArgv0(extension, nil); got != extension.ID {
		t.Fatalf("argv0 without command=%q want %q", got, extension.ID)
	}
}
