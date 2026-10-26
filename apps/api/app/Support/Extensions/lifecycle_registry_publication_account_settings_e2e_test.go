package extensionsruntime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	identity "github.com/zhuchunshu/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/zhuchunshu/sforum/apps/api/app/Models/SiteChrome"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	navigationregistry "github.com/zhuchunshu/sforum/apps/api/app/Support/NavigationRegistry"
)

// accountSettingsProbeManifest 是插件作者会真正写进 sforum.extension.json 的形态：
// 一条 permission + ownerResource 过滤的个人设置入口，以及一条 href 越界的入口。
// 两条都刻意省略 visibility —— 默认 authenticated 必须由发布层给出。
const accountSettingsProbeManifest = `{
  "manifestVersion": 3,
  "id": "registry.demo",
  "name": "Account settings probe",
  "description": "Personal settings navigation probe.",
  "url": "https://example.com/account-settings-probe",
  "author": {"name": "SForum"},
  "version": "1.0.0",
  "type": "plugin",
  "sforumVersion": ">=0.1.0",
  "permissionDefinitions": [
    {"key": "registry.demo.apps", "contractVersion": "registry.demo.apps@1", "label": "Apps", "description": "Show the personal settings entry.", "assignmentPolicy": "host"}
  ],
  "navigation": [
    {"id": "registry.demo.navigation.apps", "contractVersion": "registry.demo.navigation.apps@1", "kind": "account-settings", "action": "add", "label": "OAuth2 应用", "href": "/settings/oauth-apps", "permission": "registry.demo.apps", "ownerResource": "registry.demo.resource", "order": 50},
    {"id": "registry.demo.navigation.unsafe", "contractVersion": "registry.demo.navigation.unsafe@1", "kind": "account-settings", "action": "add", "label": "Unsafe", "href": "/private", "order": 60}
  ]
}`

// 端到端：manifest 声明 → 生命周期发布 → ResolveAccountSettingsNavigation。
// 与 navigation_registry_service_test.go 不同，这里不直接构造 Registry 声明：
// 声明先过 manifest 契约校验，再经真实生命周期发布路径进入 Registry。
func TestAccountSettingsNavigationEndToEndFromManifestDeclaration(t *testing.T) {
	ctx := context.Background()

	// 1) manifest 层：作者写的声明必须先通过 JSON Schema 与语义校验。
	body := []byte(accountSettingsProbeManifest)
	if err := extensionmanifest.ValidateV3JSONSchema(body); err != nil {
		t.Fatalf("probe manifest rejected by JSON Schema: %v", err)
	}
	var declared extensionmanifest.Manifest
	if err := json.Unmarshal(body, &declared); err != nil {
		t.Fatal(err)
	}
	if err := extensionmanifest.Validate(declared); err != nil {
		t.Fatalf("probe manifest rejected by semantic validation: %v", err)
	}
	if len(declared.Navigation) != 2 ||
		declared.Navigation[0].Kind != navigationregistry.NavigationKindAccountSettings ||
		declared.Navigation[0].Visibility != "" {
		t.Fatalf("probe manifest navigation = %#v", declared.Navigation)
	}

	// 2) 发布层：把已校验的声明挂到 exact-artifact 包骨架上，走真实生命周期发布路径。
	extension := lifecycleRegistryTestExtension(t, "1.0.0", strings.Repeat("a", 64), 501, "/account-e2e")
	extension.Manifest.Navigation = append([]extensions.ManifestNavigation(nil), declared.Navigation...)
	extension.Manifest.Regions = nil
	extension.Manifest.Permissions = declared.Permissions
	extension.Manifest.PermissionDefinitions = declared.PermissionDefinitions
	extension = writeExtensionManifest(t, extension)

	material := lifecycleNavigationTestMaterial(t, extension, "account-e2e-runtime", strings.Repeat("b", 64))
	registry := navigationregistry.New()
	if _, err := registry.Publish(navigationregistry.CorePublication()); err != nil {
		t.Fatal(err)
	}
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Navigation: registry})
	if err := boundary.reconcileNavigation(ctx, extension.ID, nil, nil, &material); err != nil {
		t.Fatalf("publish account-settings navigation: %v", err)
	}
	active, found := registry.SnapshotPublication(extension.ID)
	if !found || len(active.Navigation) != 2 {
		t.Fatalf("registry snapshot = %#v, found=%t", active, found)
	}
	for _, item := range active.Navigation {
		if item.Visibility != navigationregistry.VisibilityAuthenticated {
			t.Fatalf("published visibility = %#v", item)
		}
	}

	// 3) 投影层：真实 ResolveAccountSettingsNavigation。
	service := sitechrome.NewService(nil)
	service.WithNavigationRegistry(registry).WithNavigationRuntime(accountSettingsRuntime{}, accountSettingsRuntime{})

	granted := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{"registry.demo.apps": true}}
	items, err := service.ResolveAccountSettingsNavigation(ctx, granted, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("permission actor items = %#v", items)
	}
	item := items[0]
	if item.ID != "registry.demo.navigation.apps" || item.ExtensionID != extension.ID ||
		item.Label != "OAuth2 应用" || item.Href != "/settings/oauth-apps" || item.Order != 50 {
		t.Fatalf("resolved item = %#v", item)
	}

	// 无权限的已登录用户拿不到（permission 与 ownerResource 都不满足）。
	denied := identity.Actor{ID: 8, Status: identity.UserStatusActive}
	if deniedItems, err := service.ResolveAccountSettingsNavigation(ctx, denied, "zh-CN"); err != nil || len(deniedItems) != 0 {
		t.Fatalf("credential-less actor items = %#v err=%v", deniedItems, err)
	}
	// 游客拿不到。
	if guestItems, err := service.ResolveAccountSettingsNavigation(ctx, identity.Actor{}, "zh-CN"); err != nil || len(guestItems) != 0 {
		t.Fatalf("guest items = %#v err=%v", guestItems, err)
	}

	// ownerResource 是 permission 之外的另一条放行路径。
	service.WithAccountSettingsResourceOwner(accountSettingsResourceOwner{keys: []string{"registry.demo.resource"}})
	owner := identity.Actor{ID: 9, Status: identity.UserStatusActive}
	ownerItems, err := service.ResolveAccountSettingsNavigation(ctx, owner, "zh-CN")
	if err != nil || len(ownerItems) != 1 || ownerItems[0].ID != "registry.demo.navigation.apps" {
		t.Fatalf("owned-resource actor items = %#v err=%v", ownerItems, err)
	}
	// 不拥有该 key 的用户仍然拿不到。
	service.WithAccountSettingsResourceOwner(accountSettingsResourceOwner{keys: []string{"registry.demo.other"}})
	if otherItems, err := service.ResolveAccountSettingsNavigation(ctx, owner, "zh-CN"); err != nil || len(otherItems) != 0 {
		t.Fatalf("non-owner items = %#v err=%v", otherItems, err)
	}
}

// href 越界的 account-settings 声明在 Registry 层合法，但必须被个人设置投影丢弃
// （safeAccountSettingsHref 只放行 /settings/* 与 /extensions/*）。
func TestAccountSettingsNavigationEndToEndDropsUnsafeHref(t *testing.T) {
	ctx := context.Background()
	extension := accountSettingsTestExtension(t, "1.0.0", strings.Repeat("c", 64), 502, extensions.ManifestNavigation{
		Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd,
		Label: "Unsafe", Href: "/private",
	})
	material := lifecycleNavigationTestMaterial(t, extension, "account-e2e-unsafe", strings.Repeat("d", 64))
	registry := navigationregistry.New()
	boundary := NewPostgresLifecycleBoundaryRegistries(LifecycleRegistryBoundaryConfig{Navigation: registry})
	if err := boundary.reconcileNavigation(ctx, extension.ID, nil, nil, &material); err != nil {
		t.Fatal(err)
	}
	active, found := registry.SnapshotPublication(extension.ID)
	if !found || len(active.Navigation) != 1 || active.Navigation[0].Href != "/private" {
		t.Fatalf("registry accepted payload = %#v, found=%t", active, found)
	}
	service := sitechrome.NewService(nil)
	service.WithNavigationRegistry(registry).WithNavigationRuntime(accountSettingsRuntime{}, accountSettingsRuntime{})
	actor := identity.Actor{ID: 7, Status: identity.UserStatusActive}
	if items, err := service.ResolveAccountSettingsNavigation(ctx, actor, "zh-CN"); err != nil || len(items) != 0 {
		t.Fatalf("unsafe href leaked into personal settings: %#v err=%v", items, err)
	}
}

// accountSettingsRuntime 是 composer 需要的最小 RuntimeAdmission + RuntimeRenderer：
// 声明式 account-settings 项只要求 exact runtime 可用，不要求渲染输出。
type accountSettingsRuntime struct{}

func (accountSettingsRuntime) Available(navigationregistry.Artifact) bool { return true }

func (accountSettingsRuntime) Acquire(ctx context.Context, artifact navigationregistry.Artifact) (navigationregistry.RuntimeLease, error) {
	return accountSettingsLease{ctx: ctx, runtimeID: artifact.RuntimeInstanceID}, nil
}

func (accountSettingsRuntime) RenderNavigation(context.Context, navigationregistry.RuntimeInvocation) (navigationregistry.RuntimeOutput, error) {
	return navigationregistry.RuntimeOutput{}, nil
}

func (accountSettingsRuntime) RenderRegion(context.Context, navigationregistry.RuntimeInvocation) (navigationregistry.RuntimeOutput, error) {
	return navigationregistry.RuntimeOutput{}, nil
}

type accountSettingsLease struct {
	ctx       context.Context
	runtimeID string
}

func (l accountSettingsLease) Context() context.Context  { return l.ctx }
func (l accountSettingsLease) RuntimeInstanceID() string { return l.runtimeID }
func (l accountSettingsLease) Release()                  {}

type accountSettingsResourceOwner struct{ keys []string }

func (o accountSettingsResourceOwner) OwnedResourceKeys(context.Context, identity.Actor) ([]string, error) {
	return o.keys, nil
}
