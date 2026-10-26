package extensions

import (
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
)

// 可执行平台闸门的测试。合成 ELF 头只用来表达「这份后端是为哪个平台构建的」，
// 判定本身读的就是文件头，不执行任何包代码。

// validManifest 里 backend/plugin 的占位摘要；替换成真实字节的摘要才能通过
// 清单的 packageFiles 校验。
const placeholderBackendDigest = "a8076d3d28d21e02012b20eaf7dbf75409a6277134439025f282e368e3305abf"

func syntheticELFFor(t *testing.T, machine elf.Machine) []byte {
	t.Helper()
	buf := make([]byte, 64)
	copy(buf, "\x7fELF")
	buf[elf.EI_CLASS] = byte(elf.ELFCLASS64)
	buf[elf.EI_DATA] = byte(elf.ELFDATA2LSB)
	buf[elf.EI_VERSION] = byte(elf.EV_CURRENT)
	le := binary.LittleEndian
	le.PutUint16(buf[16:], uint16(elf.ET_EXEC))
	le.PutUint16(buf[18:], uint16(machine))
	le.PutUint32(buf[20:], 1)
	le.PutUint16(buf[52:], 64)
	return buf
}

func manifestWithBackendDigest(manifest string, body []byte) string {
	sum := sha256.Sum256(body)
	return strings.ReplaceAll(manifest, placeholderBackendDigest, hex.EncodeToString(sum[:]))
}

// hostELFMachine 把宿主架构映射成 ELF machine，映射不到时跳过用例：
// 宿主架构不在 ELF 家族里就无从构造「匹配」的样本。
func hostELFMachine(t *testing.T) elf.Machine {
	t.Helper()
	switch runtime.GOARCH {
	case "amd64":
		return elf.EM_X86_64
	case "arm64":
		return elf.EM_AARCH64
	case "386":
		return elf.EM_386
	case "arm":
		return elf.EM_ARM
	case "riscv64":
		return elf.EM_RISCV
	case "s390x":
		return elf.EM_S390
	case "loong64":
		return elf.EM_LOONGARCH
	default:
		t.Skipf("no ELF machine mapping for host arch %s", runtime.GOARCH)
		return elf.EM_NONE
	}
}

// otherELFMachine 返回一个确定不属于当前宿主的 ELF machine。
func otherELFMachine(t *testing.T) elf.Machine {
	t.Helper()
	if hostELFMachine(t) == elf.EM_AARCH64 {
		return elf.EM_X86_64
	}
	return elf.EM_AARCH64
}

// pluginWithBackendEntry 给测试插件装上 backend entry，再落一份可执行制品。
func pluginWithBackendEntry(t *testing.T, item Extension, body string) Extension {
	t.Helper()
	item.Manifest.Backend.Entry = "backend/plugin"
	item = withInstalledPackage(t, item)
	target := filepath.Join(PackageContentRoot(item), filepath.FromSlash(item.Manifest.Backend.Entry))
	if err := os.WriteFile(target, []byte(body), 0o755); err != nil {
		t.Fatalf("write backend fixture: %v", err)
	}
	return item
}

func TestInstallRejectsArchiveBuiltForAnotherPlatform(t *testing.T) {
	withCoreVersion(t, "3.0.16")
	store := &fakeExtensionStore{}
	service := newLifecycleService(store, t.TempDir(), &fakeRuntimeManager{})
	body := syntheticELFFor(t, otherELFMachine(t))

	_, err := service.InstallOrUpgradeArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "package.zip",
		Data: extensionArchive(t, manifestWithBackendDigest(validManifest("cross.arch", TypePlugin), body), zipFile{
			name: "backend/plugin",
			body: string(body),
			mode: 0o755,
		}),
	})
	if !errors.Is(err, ErrExecutablePlatformMismatch) {
		t.Fatalf("expected ErrExecutablePlatformMismatch, got %v", err)
	}
	var mismatch *ExecutablePlatformMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected typed error, got %T", err)
	}
	if mismatch.Stage != CoreCompatibilityStageInstall {
		t.Fatalf("stage = %q, want install", mismatch.Stage)
	}
	if mismatch.Entry != "backend/plugin" {
		t.Fatalf("entry = %q, want backend/plugin", mismatch.Entry)
	}
	if mismatch.Required == "" || mismatch.Host == "" || mismatch.Required == mismatch.Host {
		t.Fatalf("required/host must name two different platforms: %#v", mismatch)
	}
	// 闸门在落盘之前：错架构的包不能留下一个永远起不来的候选。
	if store.saved.Manifest.ID != "" {
		t.Fatalf("platform-incompatible package must not be persisted: %#v", store.saved)
	}
}

func TestArchivePlatformGateAcceptsMatchingHostPlatform(t *testing.T) {
	manifest := Manifest{Type: TypePlugin, Backend: ManifestBackend{Entry: "backend/plugin"}}
	files := []extensionpackage.File{{
		Path: "backend/plugin",
		Body: syntheticELFFor(t, hostELFMachine(t)),
	}}
	if err := requireArchiveExecutablePlatform(manifest, files); err != nil {
		t.Fatalf("matching host platform must pass: %v", err)
	}
}

func TestArchivePlatformGateSkipsThemesAndEntrylessPlugins(t *testing.T) {
	files := []extensionpackage.File{{
		Path: "backend/plugin",
		Body: syntheticELFFor(t, otherELFMachine(t)),
	}}
	if err := requireArchiveExecutablePlatform(Manifest{Type: TypeTheme}, files); err != nil {
		t.Fatalf("themes have no executable backend: %v", err)
	}
	if err := requireArchiveExecutablePlatform(Manifest{Type: TypePlugin}, files); err != nil {
		t.Fatalf("plugins without a backend entry are not platform bound: %v", err)
	}
}

func TestArchivePlatformGateAllowsUnrecognizedBackendPayload(t *testing.T) {
	// D8：探测不出目标平台就不拦截，交给 Core 原有的进程启动诊断。
	manifest := Manifest{Type: TypePlugin, Backend: ManifestBackend{Entry: "backend/plugin"}}
	for name, body := range map[string]string{
		"shell script": "#!/bin/sh\nexec ./server\n",
		"empty":        "",
	} {
		files := []extensionpackage.File{{Path: "backend/plugin", Body: []byte(body)}}
		if err := requireArchiveExecutablePlatform(manifest, files); err != nil {
			t.Fatalf("%s: unknown payload must not be blocked: %v", name, err)
		}
	}
}

func TestEnableRejectsArtifactBuiltForAnotherPlatform(t *testing.T) {
	item := pluginWithBackendEntry(t, uploadedExtension("cross.arch", TypePlugin),
		string(syntheticELFFor(t, otherELFMachine(t))))
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	_, err := service.Enable(context.Background(), extensionManager(), item.ID, EnableInput{})
	if !errors.Is(err, ErrExecutablePlatformMismatch) {
		t.Fatalf("expected ErrExecutablePlatformMismatch, got %v", err)
	}
	var mismatch *ExecutablePlatformMismatchError
	if !errors.As(err, &mismatch) || mismatch.Stage != CoreCompatibilityStageRuntime {
		t.Fatalf("mismatch detail = %#v", mismatch)
	}
	if store.enabledID != "" {
		t.Fatalf("platform-incompatible extension must not run: %q", store.enabledID)
	}
}

func TestPreflightKeepsScriptBackendWorking(t *testing.T) {
	// 宿主原生包与脚本型后端照旧：脚本没有可执行头，闸门放行。
	item := pluginWithBackendEntry(t, uploadedExtension("script.backend", TypePlugin),
		"#!/bin/sh\nexec ./server\n")
	if err := requireEnablePreflight(item); err != nil {
		t.Fatalf("script backends must stay enableable: %v", err)
	}
}

func TestPreflightRejectsMismatchedBuiltinArtifact(t *testing.T) {
	// 内置包对 coreVersion 豁免，但架构不豁免：错架构的内置包同样跑不起来。
	item := protectedBuiltinExtension("sforum.cross-arch", TypePlugin)
	item.Status = StatusInstalled
	item = pluginWithBackendEntry(t, item, string(syntheticELFFor(t, otherELFMachine(t))))

	if err := requireEnablePreflight(item); !errors.Is(err, ErrExecutablePlatformMismatch) {
		t.Fatalf("expected builtin artifact to be platform-checked, got %v", err)
	}
}

func TestPreflightAcceptsMatchingHostPlatform(t *testing.T) {
	item := pluginWithBackendEntry(t, uploadedExtension("native.arch", TypePlugin),
		string(syntheticELFFor(t, hostELFMachine(t))))
	if err := requireEnablePreflight(item); err != nil {
		t.Fatalf("matching host platform must pass preflight: %v", err)
	}
}

func TestPlatformGateToleratesMissingBackendEntryFile(t *testing.T) {
	// 制品不在位由 requireArtifactAvailable 给出结论；平台闸门不抢它的话。
	item := pluginWithBackendEntry(t, uploadedExtension("gone.backend", TypePlugin),
		string(syntheticELFFor(t, otherELFMachine(t))))
	target := filepath.Join(PackageContentRoot(item), filepath.FromSlash(item.Manifest.Backend.Entry))
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove backend fixture: %v", err)
	}
	if err := requireExecutablePlatform(item); err != nil {
		t.Fatalf("missing entry must not be reported as a platform mismatch: %v", err)
	}
}
