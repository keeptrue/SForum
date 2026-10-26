package extensionsruntime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	extensionpackage "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionPackage"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

// writeExtensionManifest 把当前 Manifest 写回包目录并重算 PackageDigest，
// 保证「磁盘上的 manifest」与「发布用的 Manifest」始终一致。
func writeExtensionManifest(t *testing.T, extension extensions.Extension) extensions.Extension {
	t.Helper()
	document, err := json.Marshal(extension.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extension.PackagePath, extensionmanifest.ManifestFileName), document, 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := extensionpackage.DigestTree(extension.PackagePath)
	if err != nil {
		t.Fatal(err)
	}
	extension.PackageDigest = digest
	return extension
}

// accountSettingsTestExtension 在 registry.demo 上声明一条导航项，并把 Manifest 写回包目录后
// 重算 PackageDigest（与 lifecycleNavigationTestExtension 同构，只把声明换成调用方给定的 kind）。
func accountSettingsTestExtension(
	t *testing.T,
	version, seed string,
	versionID int64,
	declaration extensions.ManifestNavigation,
) extensions.Extension {
	t.Helper()
	extension := lifecycleRegistryTestExtension(t, version, seed, versionID, "/account-"+strings.ReplaceAll(version, ".", "-"))
	declaration.ID = extension.ID + ".navigation.apps"
	declaration.ContractVersion = declaration.ID + "@1"
	extension.Manifest.Navigation = []extensions.ManifestNavigation{declaration}
	extension.Manifest.Regions = nil
	return writeExtensionManifest(t, extension)
}

// 省略 visibility 时，account-settings 必须被发布层抬升为 authenticated，
// 且该默认值必须冻结进 Registry 快照（不是只活在内存里的 Manifest）。
func TestLifecycleAccountSettingsNavigationPublicationDefaultsToAuthenticated(t *testing.T) {
	extension := accountSettingsTestExtension(t, "1.0.0", strings.Repeat("a", 64), 401, extensions.ManifestNavigation{
		Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd,
		Label: "Apps", Href: "/settings/apps", Permission: "registry.demo.apps",
		OwnerResource: "registry.demo.resource", Order: 50,
	})
	material := lifecycleNavigationTestMaterial(t, extension, "account-runtime-default", strings.Repeat("b", 64))
	publication := material.navigationPublication
	if publication == nil || len(publication.Navigation) != 1 {
		t.Fatalf("frozen account-settings publication = %#v", publication)
	}
	item := publication.Navigation[0]
	if item.Kind != navigationregistry.NavigationKindAccountSettings ||
		item.Visibility != navigationregistry.VisibilityAuthenticated {
		t.Fatalf("account-settings default visibility = %#v", item)
	}
	// permission / ownerResource 必须原样过桥，供个人设置侧栏过滤。
	if item.Permission != "registry.demo.apps" || item.OwnerResource != "registry.demo.resource" ||
		item.Href != "/settings/apps" || item.Labels["zh-CN"] != "Apps" {
		t.Fatalf("account-settings declaration drifted across the manifest bridge: %#v", item)
	}

	registry := navigationregistry.New()
	if _, err := registry.Publish(*publication); err != nil {
		t.Fatal(err)
	}
	active, found := registry.SnapshotPublication(extension.ID)
	if !found || len(active.Navigation) != 1 ||
		active.Navigation[0].Visibility != navigationregistry.VisibilityAuthenticated {
		t.Fatalf("registry snapshot = %#v, found=%t", active, found)
	}
}

// 抬升条件必须精确：显式 public 同样抬升，显式 authenticated 保持，
// 显式 anonymous 不得被静默改写为 authenticated。
func TestLifecycleAccountSettingsNavigationPublicationHonorsExplicitVisibility(t *testing.T) {
	tests := []struct {
		declared, want string
	}{
		{declared: navigationregistry.VisibilityPublic, want: navigationregistry.VisibilityAuthenticated},
		{declared: navigationregistry.VisibilityAuthenticated, want: navigationregistry.VisibilityAuthenticated},
		{declared: navigationregistry.VisibilityAnonymous, want: navigationregistry.VisibilityAnonymous},
	}
	for _, test := range tests {
		t.Run(test.declared, func(t *testing.T) {
			extension := accountSettingsTestExtension(t, "1.0.0", strings.Repeat("c", 64), 402, extensions.ManifestNavigation{
				Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd,
				Label: "Apps", Href: "/settings/apps", Visibility: test.declared,
			})
			material := lifecycleNavigationTestMaterial(t, extension, "account-runtime-"+test.declared, strings.Repeat("d", 64))
			if material.navigationPublication == nil || len(material.navigationPublication.Navigation) != 1 {
				t.Fatalf("frozen publication = %#v", material.navigationPublication)
			}
			if got := material.navigationPublication.Navigation[0].Visibility; got != test.want {
				t.Fatalf("declared %q published as %q, want %q", test.declared, got, test.want)
			}
		})
	}
}

// 回归：新增 kind 不得改变既有 6 个 kind 的默认可见性（仍为 public）。
func TestLifecycleNavigationPublicationKeepsLegacyKindDefaults(t *testing.T) {
	for index, kind := range []string{
		navigationregistry.NavigationKindMenu, navigationregistry.NavigationKindItem,
		navigationregistry.NavigationKindBreadcrumb, navigationregistry.NavigationKindHeader,
		navigationregistry.NavigationKindFooter, navigationregistry.NavigationKindSidebar,
	} {
		t.Run(kind, func(t *testing.T) {
			extension := accountSettingsTestExtension(t, "1.0.0", strings.Repeat("e", 64), int64(410+index), extensions.ManifestNavigation{
				Kind: kind, Action: navigationregistry.ActionAdd, Label: "Legacy", Href: "/legacy",
			})
			material := lifecycleNavigationTestMaterial(t, extension, "legacy-runtime-"+kind, strings.Repeat("f", 64))
			if material.navigationPublication == nil || len(material.navigationPublication.Navigation) != 1 {
				t.Fatalf("frozen publication = %#v", material.navigationPublication)
			}
			if got := material.navigationPublication.Navigation[0].Visibility; got != navigationregistry.VisibilityPublic {
				t.Fatalf("legacy kind %q default visibility = %q, want public", kind, got)
			}
		})
	}
}
