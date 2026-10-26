package extensions

import (
	"context"
	"errors"
	"testing"

	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

type recordingComponentPublisher struct {
	published []string
	removed   []string
	err       error
}

func (r *recordingComponentPublisher) PublishComponentPackageRuntime(extension Extension) error {
	r.published = append(r.published, extension.ID)
	return r.err
}

func (r *recordingComponentPublisher) RemoveComponentPackageRuntime(extensionID string) (bool, error) {
	r.removed = append(r.removed, extensionID)
	return true, r.err
}

// 主题转换方法只为满足 ComponentRegistry 接口；本测试不触达主题路径。
func (r *recordingComponentPublisher) ValidateThemeTransition(_, _ *Extension) error { return nil }

func (r *recordingComponentPublisher) PublishThemeTransition(_, _ *Extension, _ int64) error {
	return nil
}

func (r *recordingComponentPublisher) RollbackThemeTransition(_, _ *Extension, _ int64) error {
	return nil
}

func legacyComponentProbe(id string) Extension {
	return Extension{
		ID: id, Type: TypePlugin, Version: "1.0.0", Status: StatusEnabled, PackageDigest: "digest-legacy",
		Manifest: extensionmanifest.Manifest{
			ManifestVersion: 3, ID: id, Version: "1.0.0", Type: TypePlugin,
			Components: []extensionmanifest.ManifestComponent{{
				ID: id + ".body", ContractVersion: id + ".body@1", Action: "add",
				SSRTemplate: id + ".template.default",
			}},
		},
	}
}

// legacy 启用路径的组件包运行时收敛：只有公开 L2 组件、没有可执行后端的包不会走
// Lifecycle V2，因此必须在启用成功后补齐同一步骤（否则要等下一次 Host 启动）。
func TestPublishLegacyComponentRuntimeConvergesForComponentPackages(t *testing.T) {
	publisher := &recordingComponentPublisher{}
	core := &serviceCore{componentRegistry: publisher}

	publishLegacyComponentRuntime(context.Background(), core, legacyComponentProbe("acme.legacy.components"))
	if len(publisher.published) != 1 || publisher.published[0] != "acme.legacy.components" {
		t.Fatalf("published = %v; want the component package", publisher.published)
	}

	// 无组件声明 / 未启用 / 非插件类型不触发发布。
	withoutComponents := Extension{
		ID: "acme.legacy.plain", Type: TypePlugin, Version: "1.0.0",
		Status: StatusEnabled, PackageDigest: "digest-plain",
	}
	publishLegacyComponentRuntime(context.Background(), core, withoutComponents)
	disabled := legacyComponentProbe("acme.legacy.disabled")
	disabled.Status = StatusDisabled
	publishLegacyComponentRuntime(context.Background(), core, disabled)
	if len(publisher.published) != 1 {
		t.Fatalf("unexpected publications: %v", publisher.published)
	}

	// 注册表未实现窄接口（例如只实现主题转换的测试替身）时静默跳过。
	publishLegacyComponentRuntime(context.Background(), &serviceCore{}, legacyComponentProbe("acme.legacy.noregistry"))
	if len(publisher.published) != 1 {
		t.Fatalf("publication happened without a publisher: %v", publisher.published)
	}

	// 移除对称，且对空 id 是空操作。
	removeLegacyComponentRuntime(context.Background(), core, "acme.legacy.components")
	removeLegacyComponentRuntime(context.Background(), core, "")
	if len(publisher.removed) != 1 || publisher.removed[0] != "acme.legacy.components" {
		t.Fatalf("removed = %v", publisher.removed)
	}
}

// 发布失败不阻断启用：调用发生在启用已提交之后，最坏退回「下次启动才生效」的既有行为，
// 因此失败只记录事件、不返回错误（函数本身没有返回值）。
func TestPublishLegacyComponentRuntimeSwallowsPublicationFailure(t *testing.T) {
	publisher := &recordingComponentPublisher{err: errors.New("registry unavailable")}
	events := []string{}
	core := &serviceCore{
		componentRegistry: publisher,
		store:             &orderedQueryStore{fakeExtensionStore: newFakeExtensionStore(nil), events: &events},
	}

	publishLegacyComponentRuntime(context.Background(), core, legacyComponentProbe("acme.legacy.failing"))
	if len(publisher.published) != 1 {
		t.Fatalf("publication attempt missing: %v", publisher.published)
	}
	removeLegacyComponentRuntime(context.Background(), core, "acme.legacy.failing")
	if len(publisher.removed) != 1 {
		t.Fatalf("removal attempt missing: %v", publisher.removed)
	}
}
