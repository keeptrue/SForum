package extensions

import (
	"context"
	"testing"

	"github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

// 允许侧 / 拒绝侧：插件页面贡献必须在 **enable 期**就带上活动 runtime instance id。
//
// 为什么这条测试存在：此前只有启动恢复路径（`restoreExactPluginPagePublications`）会写
// `RuntimeInstanceID`，启用路径不写——于是「刚启用、还没重启 API」时页面贡献缺 runtime
// instance，loader 的精确制品检查直接以 `pages: exact plugin runtime unavailable` 回退，
// 必须重启一次插件页才生效。这是作者第一步就会撞到的体验缺陷。
func TestPageRegistryAdapterBindsPluginRuntimeInstanceAtEnable(t *testing.T) {
	extension := pluginPageRegistryFixture(t, "plugin.runtime.binding")
	registry := pages.NewRegistry(pages.NewMemoryStore())

	t.Run("exact active instance is bound", func(t *testing.T) {
		adapter := NewPageRegistryAdapter(registry).
			WithRuntimeInstances(stubPageRuntimeInstances{
				instanceID: "instance-1", version: extension.Version, digest: extension.PackageDigest,
			})
		if err := adapter.RegisterPluginPackage(context.Background(), extension); err != nil {
			t.Fatal(err)
		}
		contribution, ok := registry.ResolveAddedPath("/runtime-binding")
		if !ok {
			t.Fatal("plugin page contribution was not registered")
		}
		if contribution.RuntimeInstanceID != "instance-1" {
			t.Fatalf("contribution runtime instance = %q", contribution.RuntimeInstanceID)
		}
		// 注册表只有在贡献携带 runtime instance 时才会走 admission；
		// 这里断言它的确被当作需要 runtime 的贡献（id 非空即会校验）。
		if contribution.ExtensionID != extension.ID || contribution.Version != extension.Version {
			t.Fatalf("contribution artifact = %#v", contribution)
		}
	})

	t.Run("mismatched artifact stays unbound", func(t *testing.T) {
		other := pages.NewRegistry(pages.NewMemoryStore())
		adapter := NewPageRegistryAdapter(other).
			WithRuntimeInstances(stubPageRuntimeInstances{
				instanceID: "instance-2", version: extension.Version, digest: "other-digest",
			})
		if err := adapter.RegisterPluginPackage(context.Background(), extension); err != nil {
			t.Fatal(err)
		}
		contribution, ok := other.ResolveAddedPath("/runtime-binding")
		if !ok {
			t.Fatal("plugin page contribution was not registered")
		}
		// 不匹配时保持为空：不伪造 runtime 身份，请求期由 admission 决定可见性。
		if contribution.RuntimeInstanceID != "" {
			t.Fatalf("mismatched artifact bound runtime instance %q", contribution.RuntimeInstanceID)
		}
	})

	t.Run("no runtime source stays unbound", func(t *testing.T) {
		plain := pages.NewRegistry(pages.NewMemoryStore())
		if err := NewPageRegistryAdapter(plain).RegisterPluginPackage(context.Background(), extension); err != nil {
			t.Fatal(err)
		}
		contribution, ok := plain.ResolveAddedPath("/runtime-binding")
		if !ok || contribution.RuntimeInstanceID != "" {
			t.Fatalf("unwired adapter bound runtime instance: %#v ok=%v", contribution, ok)
		}
	})
}

// stubPageRuntimeInstances 按传入的版本/摘要精确匹配才返回 instance id。
type stubPageRuntimeInstances struct {
	instanceID string
	version    string
	digest     string
}

func (s stubPageRuntimeInstances) ActiveRuntimeInstanceID(extensionID, version, packageDigest string) (string, bool) {
	if extensionID == "" || s.instanceID == "" {
		return "", false
	}
	if version != "" && s.version != version {
		return "", false
	}
	if packageDigest != "" && s.digest != packageDigest {
		return "", false
	}
	return s.instanceID, true
}

func pluginPageRegistryFixture(t *testing.T, id string) Extension {
	t.Helper()
	root := t.TempDir()
	writeThemeRuntimeExtensionFile(t, root, "theme.json",
		`{"pages":[{"id":"`+id+`.page","action":"add","path":"/runtime-binding","template":"templates/page.html",`+
			`"contract":"`+id+`.page@1","data":{"source":"plugin","route":"/page-data"}}]}`)
	writeThemeRuntimeExtensionFile(t, root, "templates/page.html", `<main>page</main>`)
	return Extension{
		ID: id, Type: TypePlugin, Status: StatusEnabled, Version: "1.0.0",
		PackageDigest: "digest-" + id, PackagePath: root,
	}
}
