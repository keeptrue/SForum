package extensionsruntime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
)

func (b *PostgresLifecycleBoundaryRegistries) validateComponentTransition(
	source, target *lifecycleRegistryMaterial,
) error {
	extensionID := lifecycleComponentExtensionID(source, target)
	if extensionID == "" {
		return ErrLifecycleRegistryPublicationInvalid
	}
	current, exists := b.components.RuntimeSnapshot(extensionID)
	if exists && !componentRuntimeSnapshotAllowed(current, source, target) {
		return fmt.Errorf(
			"%w: component runtime: %w", ErrLifecycleRegistryPublicationConflict, ErrComponentRegistryConflict,
		)
	}
	if target != nil && len(target.extension.Manifest.Components) > 0 {
		err := b.components.replaceRuntime(
			target.extension,
			componentPackageRuntimeInstanceID(target.extension),
			false,
			true,
			func(registration componentRuntimeRegistration) bool {
				return componentRuntimeRegistrationAllowed(registration, source, target)
			},
		)
		return wrapLifecycleComponentError("validate component registry", err)
	}
	if !exists {
		return nil
	}
	return wrapLifecycleComponentError(
		"validate component registry removal",
		b.components.ValidateRemoveRuntime(extensionID, current.InstanceID),
	)
}

func (b *PostgresLifecycleBoundaryRegistries) reconcileComponents(
	extensionID string,
	source, target, desired *lifecycleRegistryMaterial,
) error {
	if desired != nil && len(desired.extension.Manifest.Components) > 0 {
		err := b.components.replaceRuntime(
			desired.extension,
			componentPackageRuntimeInstanceID(desired.extension),
			true,
			true,
			func(registration componentRuntimeRegistration) bool {
				return componentRuntimeRegistrationAllowed(registration, source, target)
			},
		)
		if err != nil {
			return wrapLifecycleComponentError("publish component registry", err)
		}
		// 组件注册表发布成功后同步编译包本地 SSR；失败回滚组件注册不在此层处理，
		// 由下一次 reconcile 重试。自定义 PluginRenderer 时 PublishPackageSSR 仍更新缓存。
		if b.componentSSR != nil {
			if ssrErr := b.componentSSR.PublishPackageSSR(desired.extension); ssrErr != nil {
				return wrapLifecycleComponentError("publish package-local component SSR", ssrErr)
			}
		}
		return nil
	}
	current, exists := b.components.RuntimeSnapshot(extensionID)
	if !exists {
		if b.componentSSR != nil {
			b.componentSSR.RemovePackageSSR(extensionID, "")
		}
		return nil
	}
	if !componentRuntimeSnapshotAllowed(current, source, target) {
		return fmt.Errorf(
			"%w: component runtime: %w", ErrLifecycleRegistryPublicationConflict, ErrComponentRegistryConflict,
		)
	}
	removed, err := b.components.RemoveRuntime(extensionID, current.InstanceID)
	if err != nil {
		return wrapLifecycleComponentError("unpublish component registry", err)
	}
	if !removed {
		return fmt.Errorf(
			"%w: component runtime disappeared", ErrLifecycleRegistryPublicationConflict,
		)
	}
	if b.componentSSR != nil {
		b.componentSSR.RemovePackageSSR(extensionID, current.Extension.PackageDigest)
	}
	return nil
}

func lifecycleComponentExtensionID(materials ...*lifecycleRegistryMaterial) string {
	for index := len(materials) - 1; index >= 0; index-- {
		if materials[index] != nil {
			return materials[index].extension.ID
		}
	}
	return ""
}

func componentRuntimeSnapshotAllowed(
	snapshot ComponentRuntimeSnapshot,
	materials ...*lifecycleRegistryMaterial,
) bool {
	return componentRuntimeAllowed(snapshot.Extension, snapshot.InstanceID, materials...)
}

func componentRuntimeRegistrationAllowed(
	registration componentRuntimeRegistration,
	materials ...*lifecycleRegistryMaterial,
) bool {
	return componentRuntimeAllowed(registration.extension, registration.instanceID, materials...)
}

func componentRuntimeAllowed(
	extension extensions.Extension,
	instanceID string,
	materials ...*lifecycleRegistryMaterial,
) bool {
	// Declarative component publication is fenced by the Host package identity;
	// a lifecycle process binding is deliberately never accepted here.
	for _, material := range materials {
		if material == nil {
			continue
		}
		exact := material.extension
		if extension.ID == exact.ID && extension.Version == exact.Version && extension.Type == exact.Type &&
			extension.PackageDigest == exact.PackageDigest &&
			instanceID == componentPackageRuntimeInstanceID(exact) {
			return true
		}
	}
	return false
}

func wrapLifecycleComponentError(action string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrComponentRegistryConflict) {
		return fmt.Errorf("%w: %s: %w", ErrLifecycleRegistryPublicationConflict, action, err)
	}
	return fmt.Errorf("%s: %w", action, err)
}

// 生命周期注册表材料的身份/摘要助手。
//
// 放在这里而不是新文件：`app/Support/Extensions` 已到 flat-directory 上限（148 个非测试
// 文件），不能再加文件；这些助手被全部 per-registry 发布路径共用，与组件发布同属
// 「材料契约」一层。
func registryMaterialDigest(material *lifecycleRegistryMaterial, extensionID string) string {
	if material != nil {
		return material.digest
	}
	raw, _ := json.Marshal(struct {
		Schema      string `json:"schema"`
		ExtensionID string `json:"extensionId"`
		State       string `json:"state"`
	}{"sforum.lifecycle.registry-plan@1", extensionID, "absent"})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func registryMaterialCompatibleDigests(material *lifecycleRegistryMaterial) []string {
	if material == nil || len(material.compatibleDigests) == 0 {
		return nil
	}
	return append([]string(nil), material.compatibleDigests...)
}
