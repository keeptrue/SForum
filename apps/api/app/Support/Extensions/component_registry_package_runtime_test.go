package extensionsruntime

import (
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// 组件包运行时发布的收敛契约：PublishComponentPackageRuntime 必须产生与 Host 启动
// （RestoreRuntimes）完全相同的注册结果，否则公开 L2 组件会在启用后一直不可用，
// 直到下一次 Host 重启。
func TestPublishComponentPackageRuntimeMatchesStartupRestore(t *testing.T) {
	extension := componentTestExtension(t, "acme.component.probe", extensions.TypePlugin,
		extensions.ManifestComponent{
			ID: "acme.component.probe.body", ContractVersion: "acme.component.probe.body@1",
			Action: extensionmanifest.ComponentActionAdd,
		})

	startup := NewComponentRegistry()
	if err := startup.RestoreRuntimes([]extensions.Extension{extension}, false); err != nil {
		t.Fatal(err)
	}
	lifecycle := NewComponentRegistry()
	if err := lifecycle.PublishComponentPackageRuntime(extension); err != nil {
		t.Fatal(err)
	}

	startupSnapshot, startupFound := startup.RuntimeSnapshot(extension.ID)
	lifecycleSnapshot, lifecycleFound := lifecycle.RuntimeSnapshot(extension.ID)
	if !startupFound || !lifecycleFound {
		t.Fatalf("registration missing: startup=%v lifecycle=%v", startupFound, lifecycleFound)
	}
	if startupSnapshot.InstanceID != lifecycleSnapshot.InstanceID {
		t.Fatalf("instance id diverged: startup=%q lifecycle=%q",
			startupSnapshot.InstanceID, lifecycleSnapshot.InstanceID)
	}
	if startupSnapshot.InstanceID != componentPackageRuntimeInstanceID(extension) {
		t.Fatalf("instance id = %q; want the package-level id", startupSnapshot.InstanceID)
	}
}

// 无组件声明、未启用、nil registry 都是空操作：调用方无需自己判断。
func TestPublishComponentPackageRuntimeIsNoOpWithoutComponents(t *testing.T) {
	registry := NewComponentRegistry()
	withoutComponents := extensions.Extension{
		ID: "acme.no.components", Type: extensions.TypePlugin, Version: "1.0.0",
		Status: extensions.StatusEnabled, PackageDigest: "digest-2",
	}
	if err := registry.PublishComponentPackageRuntime(withoutComponents); err != nil {
		t.Fatalf("no-op publication returned %v", err)
	}
	if _, found := registry.RuntimeSnapshot(withoutComponents.ID); found {
		t.Fatal("a package without component declarations must not be registered")
	}
	var nilRegistry *ComponentRegistry
	if err := nilRegistry.PublishComponentPackageRuntime(withoutComponents); err != nil {
		t.Fatalf("nil registry returned %v", err)
	}
}

// 移除是对称的：注册后可移除，重复移除返回 false 而不是错误。
func TestRemoveComponentPackageRuntimeIsIdempotent(t *testing.T) {
	extension := componentTestExtension(t, "acme.component.remove", extensions.TypePlugin,
		extensions.ManifestComponent{
			ID: "acme.component.remove.body", ContractVersion: "acme.component.remove.body@1",
			Action: extensionmanifest.ComponentActionAdd,
		})
	registry := NewComponentRegistry()
	if err := registry.PublishComponentPackageRuntime(extension); err != nil {
		t.Fatal(err)
	}
	removed, err := registry.RemoveComponentPackageRuntime(extension.ID)
	if err != nil || !removed {
		t.Fatalf("remove = (%v, %v); want (true, nil)", removed, err)
	}
	removed, err = registry.RemoveComponentPackageRuntime(extension.ID)
	if err != nil || removed {
		t.Fatalf("second remove = (%v, %v); want (false, nil)", removed, err)
	}
}
