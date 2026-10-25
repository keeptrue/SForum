package extensions

import (
	"context"
	"errors"
	"strings"
	"testing"

	platformversion "github.com/zhuchunshu/sforum/apps/api/version"
)

// withCoreVersion 把全局构建标签固定成一个可判定的值。该包测试不并行，
// 用例结束即恢复，避免污染其它用例的 dev=1.0.0 语义。
func withCoreVersion(t *testing.T, value string) {
	t.Helper()
	original := platformversion.Current
	platformversion.Current = value
	t.Cleanup(func() { platformversion.Current = original })
}

func incompatibleManifest(t *testing.T, id string, constraint string) string {
	t.Helper()
	return strings.Replace(
		validManifest(id, TypePlugin),
		`"sforumVersion": "^1.0.0"`,
		`"sforumVersion": "`+constraint+`"`,
		1,
	)
}

func TestInstallRejectsManifestThatExcludesTheRunningCore(t *testing.T) {
	withCoreVersion(t, "3.0.16")
	store := &fakeExtensionStore{}
	service := newLifecycleService(store, t.TempDir(), &fakeRuntimeManager{})

	_, err := service.InstallOrUpgradeArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "package.zip",
		Data:     extensionArchive(t, incompatibleManifest(t, "future.plugin", "^9.0.0")),
	})
	if !errors.Is(err, ErrCoreIncompatible) {
		t.Fatalf("expected ErrCoreIncompatible, got %v", err)
	}
	var incompatible *CoreIncompatibleError
	if !errors.As(err, &incompatible) {
		t.Fatalf("expected typed error, got %T", err)
	}
	if incompatible.Stage != CoreCompatibilityStageInstall ||
		incompatible.Required != "^9.0.0" || incompatible.CoreVersion != "3.0.16" {
		t.Fatalf("incompatible detail = %#v", incompatible)
	}
	// 不兼容必须在落库之前中止，不能留下一个永远启用不了的候选。
	if store.saved.Manifest.ID != "" {
		t.Fatalf("incompatible package must not be persisted: %#v", store.saved)
	}
}

func TestInstallAcceptsTheSameManifestOnAMatchingReleaseLine(t *testing.T) {
	// 同一份 ^1.0.0 清单：dev 构建（1.0.0）通过，3.x 发行版拒绝。
	store := &fakeExtensionStore{}
	service := newLifecycleService(store, t.TempDir(), &fakeRuntimeManager{})
	archive := extensionArchive(t, validManifest("expect.plugin", TypePlugin))

	if _, err := service.InstallOrUpgradeArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "package.zip", Data: archive,
	}); err != nil {
		t.Fatalf("dev build should accept ^1.0.0: %v", err)
	}

	withCoreVersion(t, "3.0.16")
	store = &fakeExtensionStore{}
	service = newLifecycleService(store, t.TempDir(), &fakeRuntimeManager{})
	if _, err := service.InstallOrUpgradeArchive(context.Background(), extensionManager(), ArchiveInput{
		FileName: "package.zip", Data: archive,
	}); !errors.Is(err, ErrCoreIncompatible) {
		t.Fatalf("3.x build must reject ^1.0.0, got %v", err)
	}
}

func TestEnableRejectsUploadedExtensionThatExcludesTheRunningCore(t *testing.T) {
	withCoreVersion(t, "3.0.16")
	item := uploadedExtension("future.plugin", TypePlugin)
	item.Manifest.SForumVersion = "^9.0.0"
	item = withInstalledPackage(t, item)
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	_, err := service.Enable(context.Background(), extensionManager(), item.ID, EnableInput{})
	if !errors.Is(err, ErrCoreIncompatible) {
		t.Fatalf("expected ErrCoreIncompatible, got %v", err)
	}
	var incompatible *CoreIncompatibleError
	if !errors.As(err, &incompatible) || incompatible.Stage != CoreCompatibilityStageRuntime {
		t.Fatalf("incompatible detail = %#v", incompatible)
	}
	if store.enabledID != "" {
		t.Fatalf("incompatible extension must not run: %q", store.enabledID)
	}
}

func TestEnableKeepsBuiltinExemptionFromManifestConstraints(t *testing.T) {
	withCoreVersion(t, "3.0.16")
	// 内置包与 core 同一个发行物；默认主题今天就写着 ^1.0.0，豁免是唯一
	// 不把发行版自己挡死的选择。
	item := protectedBuiltinExtension("sforum.legacy", TypePlugin)
	item.Manifest.SForumVersion = "^1.0.0"
	item.Status = StatusInstalled
	item = withInstalledPackage(t, item)
	store := &fakeExtensionStore{items: map[string]Extension{item.ID: item}}
	service := NewServiceWithRuntime(store, t.TempDir(), &fakeRuntimeManager{})

	if _, err := service.Enable(context.Background(), extensionManager(), item.ID, EnableInput{}); errors.Is(err, ErrCoreIncompatible) {
		t.Fatalf("builtin packages must stay exempt, got %v", err)
	}
}

func TestUnverifiableCoreVersionSkipsInsteadOfBlocking(t *testing.T) {
	withCoreVersion(t, "v3.0.16")
	item := uploadedExtension("future.plugin", TypePlugin)
	item.Manifest.SForumVersion = "^9.0.0"
	if err := requireEnablePreflight(item); err != nil {
		t.Fatalf("an invalid core label must not block installs: %v", err)
	}
}
