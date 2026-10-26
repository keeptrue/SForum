package sitechrome

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

func TestSiteChromeNavigationSafeDefaultAndLocaleCacheIsolation(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	if _, err := service.CreateNavItem(t.Context(), manageActor(), CreateNavItemInput{
		LabelZhCN: "文档", LabelEnUS: "Docs", Href: "/docs", Position: 10, Enabled: true, OpenInNewTab: true,
	}); err != nil {
		t.Fatal(err)
	}
	zh, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	en, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "en_US")
	if err != nil {
		t.Fatal(err)
	}
	if len(zh.Menus) != 1 || len(zh.Menus[0].Children) != 1 || zh.Menus[0].Children[0].Label != "文档" ||
		len(en.Menus) != 1 || en.Menus[0].Children[0].Label != "Docs" || zh.CacheKey == en.CacheKey ||
		zh.Revision != 0 || en.Locale != "en-US" || zh.Menus[0].Children[0].Attributes["open-in-new-tab"] != "true" {
		t.Fatalf("safe locale defaults zh=%#v en=%#v", zh, en)
	}
	if service.NavigationInspector() != nil {
		t.Fatal("unconfigured service exposed an inspector")
	}
	items, err := service.ListPublicNavItems(t.Context())
	if err != nil || len(items) != 1 {
		t.Fatalf("legacy public behavior changed: %#v err=%v", items, err)
	}
}

func TestSiteChromeNavigationRegistryPermissionLocaleRegionsAndCacheInvalidation(t *testing.T) {
	store := newFakeStore()
	service := NewService(store)
	created, err := service.CreateNavItem(t.Context(), manageActor(), CreateNavItemInput{
		LabelZhCN: "首页", LabelEnUS: "Home", Href: "/", Position: 5, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry)
	if service.navigation == nil || service.navigation.err != nil {
		t.Fatalf("registry configuration=%#v", service.navigation)
	}
	runtime := &siteChromeRuntime{}
	service.WithNavigationRuntime(runtime, runtime)

	plugin := siteChromePublication("plugin.chrome", 'a')
	item := navigationregistry.NavigationDeclaration{
		ID: "plugin.chrome.item.private", ContractVersion: "plugin.chrome.item.private@1",
		Kind: navigationregistry.NavigationKindItem, Action: navigationregistry.ActionAdd,
		TargetID: navigationregistry.CorePrimaryMenuID, Label: "Private",
		Labels: map[string]string{"zh-CN": "私有", "en-US": "Private"}, Href: "/private",
		Permission: "forum.private", Visibility: navigationregistry.VisibilityAuthenticated, Order: 10,
	}
	plugin.Navigation = []navigationregistry.NavigationDeclaration{item}
	plugin.Regions = []navigationregistry.RegionDeclaration{{
		ID: "plugin.chrome.region.content", ContractVersion: "plugin.chrome.region.content@1",
		Kind: navigationregistry.RegionKindContent, Action: navigationregistry.ActionAdd,
		Label: "Content", Multiple: true,
	}}
	if _, err := registry.Publish(plugin); err != nil {
		t.Fatal(err)
	}

	denied, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(denied.Menus) != 1 || hasChromeNode(denied.Menus[0].Children, item.ID) {
		t.Fatalf("guest saw protected item: %#v", denied.Menus)
	}
	actor := identity.Actor{
		ID: 42, Status: identity.UserStatusActive, Permissions: map[string]bool{"forum.private": true},
	}
	allowed, err := service.ComposePublicNavigation(t.Context(), actor, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Menus) != 1 || len(allowed.Menus[0].Children) != 2 ||
		!hasChromeNode(allowed.Menus[0].Children, item.ID) || denied.CacheKey == allowed.CacheKey {
		t.Fatalf("allowed composition=%#v", allowed)
	}
	zh, err := service.ComposePublicNavigation(t.Context(), actor, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if node := chromeNodeByID(zh.Menus[0].Children, item.ID); node == nil || node.Label != "私有" || zh.CacheKey == allowed.CacheKey {
		t.Fatalf("localized plugin item=%#v", zh)
	}
	if len(allowed.Breadcrumbs) != 1 || len(allowed.Headers) != 1 || len(allowed.Footers) != 1 ||
		len(allowed.Sidebars) != 1 || len(allowed.Regions.Menus) != 1 || len(allowed.Regions.Headers) != 1 ||
		len(allowed.Regions.Footers) != 1 || len(allowed.Regions.Sidebars) != 1 || len(allowed.Regions.Widgets) != 1 ||
		len(allowed.Regions.Content) != 1 {
		t.Fatalf("incomplete view-model families=%#v", allowed)
	}

	updatedLabel := "Start"
	if _, err := service.UpdateNavItem(t.Context(), manageActor(), UpdateNavItemInput{ID: created.ID, LabelEnUS: &updatedLabel}); err != nil {
		t.Fatal(err)
	}
	contentChanged, err := service.ComposePublicNavigation(t.Context(), actor, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if contentChanged.Revision != allowed.Revision || contentChanged.CacheKey == allowed.CacheKey ||
		contentChanged.Menus[0].Children[0].Label != "Start" {
		t.Fatalf("content digest cache evidence before=%#v after=%#v", allowed, contentChanged)
	}

	second := siteChromePublication("plugin.chrome.second", 'b')
	second.Navigation = []navigationregistry.NavigationDeclaration{{
		ID: "plugin.chrome.second.item.public", ContractVersion: "plugin.chrome.second.item.public@1",
		Kind: navigationregistry.NavigationKindItem, Action: navigationregistry.ActionAdd,
		TargetID: navigationregistry.CorePrimaryMenuID, Label: "Second", Href: "/second", Order: 20,
	}}
	beforeState := registry.CacheState()
	if _, err := registry.Publish(second); err != nil {
		t.Fatal(err)
	}
	graphChanged, err := service.ComposePublicNavigation(t.Context(), actor, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if !registry.CacheInvalidated(beforeState) || graphChanged.Revision == contentChanged.Revision ||
		graphChanged.Digest == contentChanged.Digest || graphChanged.CacheKey == contentChanged.CacheKey {
		t.Fatalf("graph cache evidence before=%#v after=%#v", contentChanged, graphChanged)
	}
	body, _ := json.Marshal(graphChanged)
	if strings.Contains(string(body), plugin.Artifact.PackageDigest) || strings.Contains(string(body), plugin.Artifact.RuntimeInstanceID) {
		t.Fatalf("public view leaked exact artifact attribution: %s", body)
	}
	inspection, err := service.NavigationInspector().Inspect(item.ID, 32)
	if err != nil || len(inspection.Traces) == 0 || inspection.Traces[0].Artifact.ExtensionID == "" {
		t.Fatalf("inspector attribution=%#v err=%v", inspection, err)
	}
}

func TestAccountSettingsNavigationUsesPermissionOrOwnedResourceAndSafePaths(t *testing.T) {
	service := NewService(newFakeStore())
	registry := navigationregistry.New()
	runtime := &siteChromeRuntime{}
	service.WithNavigationRegistry(registry).WithNavigationRuntime(runtime, runtime)
	plugin := siteChromePublication("plugin.oauth", 'a')
	plugin.Navigation = []navigationregistry.NavigationDeclaration{
		{ID: "plugin.oauth.settings.apps", ContractVersion: "plugin.oauth.settings.apps@1", Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd, Label: "Apps", Href: "/extensions/plugin.oauth/settings/apps", Visibility: navigationregistry.VisibilityAuthenticated, Permission: "oauth.apps.apply", Order: 20},
		{ID: "plugin.oauth.settings.owned", ContractVersion: "plugin.oauth.settings.owned@1", Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd, Label: "Owned", Href: "/extensions/plugin.oauth/settings/owned", Visibility: navigationregistry.VisibilityAuthenticated, OwnerResource: "oauth.app.owner", Order: 30},
		{ID: "plugin.oauth.settings.unsafe", ContractVersion: "plugin.oauth.settings.unsafe@1", Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd, Label: "Unsafe", Href: "/private", Visibility: navigationregistry.VisibilityAuthenticated, Order: 40},
	}
	if _, err := registry.Publish(plugin); err != nil {
		t.Fatal(err)
	}
	guest, err := service.ResolveAccountSettingsNavigation(t.Context(), identity.Actor{}, "en-US")
	if err != nil || len(guest) != 0 {
		t.Fatalf("guest account settings=%#v err=%v", guest, err)
	}
	permissionActor := identity.Actor{ID: 7, Status: identity.UserStatusActive, Permissions: map[string]bool{"oauth.apps.apply": true}}
	permissionItems, err := service.ResolveAccountSettingsNavigation(t.Context(), permissionActor, "en-US")
	if err != nil || len(permissionItems) != 1 || permissionItems[0].ID != "plugin.oauth.settings.apps" {
		t.Fatalf("permission account settings=%#v err=%v", permissionItems, err)
	}
	ownedActor := identity.Actor{ID: 8, Status: identity.UserStatusActive}
	service.WithAccountSettingsResourceOwner(accountSettingsResourceOwnerStub{keys: []string{"oauth.app.owner"}})
	ownedItems, err := service.ResolveAccountSettingsNavigation(t.Context(), ownedActor, "en-US")
	if err != nil || len(ownedItems) != 1 || ownedItems[0].ID != "plugin.oauth.settings.owned" {
		t.Fatalf("owned account settings=%#v err=%v", ownedItems, err)
	}
}

// 浏览器默认 Accept-Language 会带 q 值（如 "zh-CN,zh;q=0.9,en;q=0.8"）。
// 账号设置导航必须在调用侧先归一化，否则 NavigationRegistry.normalizeLocale
// 判 ErrInvalid，接口返回 500 并连带 SSR 的 /settings/* 页面 503。
func TestAccountSettingsNavigationNormalizesRawAcceptLanguageHeader(t *testing.T) {
	service := NewService(newFakeStore())
	registry := navigationregistry.New()
	runtime := &siteChromeRuntime{}
	service.WithNavigationRegistry(registry).WithNavigationRuntime(runtime, runtime)
	plugin := siteChromePublication("plugin.oauth", 'a')
	plugin.Navigation = []navigationregistry.NavigationDeclaration{{
		ID: "plugin.oauth.settings.apps", ContractVersion: "plugin.oauth.settings.apps@1",
		Kind: navigationregistry.NavigationKindAccountSettings, Action: navigationregistry.ActionAdd,
		Label: "OAuth 应用", Href: "/settings/oauth-apps",
		Visibility: navigationregistry.VisibilityAuthenticated, Permission: "oauth.apps.manage", Order: 10,
	}}
	if _, err := registry.Publish(plugin); err != nil {
		t.Fatal(err)
	}
	actor := identity.Actor{
		ID: 9, Status: identity.UserStatusActive, Permissions: map[string]bool{"oauth.apps.manage": true},
	}
	raw, err := service.ResolveAccountSettingsNavigation(t.Context(), actor, "zh-CN,zh;q=0.9,en;q=0.8")
	if err != nil {
		t.Fatalf("raw Accept-Language rejected: %v", err)
	}
	if len(raw) != 1 || raw[0].ID != "plugin.oauth.settings.apps" || raw[0].Href != "/settings/oauth-apps" {
		t.Fatalf("raw Accept-Language items=%#v", raw)
	}
	normalized, err := service.ResolveAccountSettingsNavigation(t.Context(), actor, "zh-CN")
	if err != nil {
		t.Fatal(err)
	}
	if len(normalized) != len(raw) || normalized[0].ID != raw[0].ID || normalized[0].Href != raw[0].Href {
		t.Fatalf("raw header diverged from normalized locale: raw=%#v normalized=%#v", raw, normalized)
	}
}

type accountSettingsResourceOwnerStub struct{ keys []string }

func (s accountSettingsResourceOwnerStub) OwnedResourceKeys(context.Context, identity.Actor) ([]string, error) {
	return s.keys, nil
}

func TestSiteChromeSelectedReplaceFailsClosedButOptionalRuntimeFallsBack(t *testing.T) {
	service := NewService(newFakeStore())
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry)
	runtime := &siteChromeRuntime{unavailable: map[string]bool{}}
	service.WithNavigationRuntime(runtime, runtime)
	plugin := siteChromePublication("plugin.replace", 'c')
	replace := navigationregistry.NavigationDeclaration{
		ID: "plugin.replace.header.main", ContractVersion: "plugin.replace.header.main@1",
		Kind: navigationregistry.NavigationKindHeader, Action: navigationregistry.ActionReplace,
		TargetID: navigationregistry.CoreHeaderNavigationID, Label: "Replacement", Handler: "plugin.replace.render.header",
	}
	plugin.Navigation = []navigationregistry.NavigationDeclaration{replace}
	if _, err := registry.Publish(plugin); err != nil {
		t.Fatal(err)
	}
	runtime.renderError = errors.New("crash")
	optional, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "en-US")
	if err != nil || len(optional.Headers) != 1 || optional.Headers[0].Label != "Header" {
		t.Fatalf("optional fallback=%#v err=%v", optional, err)
	}
	if _, err := registry.SelectProvider(navigationregistry.SelectProviderRequest{
		ExpectedRevision: registry.Revision(), Family: navigationregistry.ProviderFamilyNavigation,
		TargetID: navigationregistry.CoreHeaderNavigationID,
		Provider: navigationregistry.ProviderRef{ContributionID: replace.ID, Artifact: plugin.Artifact},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ComposePublicNavigation(t.Context(), identity.Actor{}, "en-US"); !errors.Is(err, navigationregistry.ErrTrustedReplace) {
		t.Fatalf("selected replacement did not fail closed: %v", err)
	}
}

type siteChromeRuntime struct {
	unavailable map[string]bool
	renderError error
}

func (r *siteChromeRuntime) Available(artifact navigationregistry.Artifact) bool {
	return r == nil || !r.unavailable[siteChromeArtifactKey(artifact)]
}

func (r *siteChromeRuntime) Acquire(ctx context.Context, artifact navigationregistry.Artifact) (navigationregistry.RuntimeLease, error) {
	if !r.Available(artifact) {
		return nil, navigationregistry.ErrRuntimeUnavailable
	}
	return siteChromeLease{ctx: ctx, runtimeID: artifact.RuntimeInstanceID}, nil
}

func (r *siteChromeRuntime) RenderNavigation(context.Context, navigationregistry.RuntimeInvocation) (navigationregistry.RuntimeOutput, error) {
	if r.renderError != nil {
		return navigationregistry.RuntimeOutput{}, r.renderError
	}
	return navigationregistry.RuntimeOutput{}, nil
}

func (r *siteChromeRuntime) RenderRegion(context.Context, navigationregistry.RuntimeInvocation) (navigationregistry.RuntimeOutput, error) {
	if r.renderError != nil {
		return navigationregistry.RuntimeOutput{}, r.renderError
	}
	return navigationregistry.RuntimeOutput{}, nil
}

type siteChromeLease struct {
	ctx       context.Context
	runtimeID string
}

func (l siteChromeLease) Context() context.Context  { return l.ctx }
func (l siteChromeLease) RuntimeInstanceID() string { return l.runtimeID }
func (siteChromeLease) Release()                    {}

func siteChromePublication(id string, digest rune) navigationregistry.Publication {
	value := strings.Repeat(string(digest), 64)
	return navigationregistry.Publication{Artifact: navigationregistry.Artifact{
		ExtensionID: id, ExtensionVersion: "1.0.0", PackageDigest: value, ImpactDigest: value,
		VersionID: 1, RuntimeInstanceID: "runtime-" + string(digest),
	}}
}

func siteChromeArtifactKey(artifact navigationregistry.Artifact) string {
	return artifact.ExtensionID + "\x00" + artifact.PackageDigest + "\x00" + artifact.RuntimeInstanceID
}

func hasChromeNode(items []ChromeNodeViewModel, id string) bool {
	return chromeNodeByID(items, id) != nil
}

func chromeNodeByID(items []ChromeNodeViewModel, id string) *ChromeNodeViewModel {
	for index := range items {
		if items[index].ID == id {
			return &items[index]
		}
	}
	return nil
}
