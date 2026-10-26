package sitechrome

import (
	"context"
	"testing"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

type memoryNavigationDocumentStore struct{ document NavigationDocument }

func (s memoryNavigationDocumentStore) ReadNavigationDocument(context.Context) (NavigationDocument, error) {
	return s.document, nil
}

type navigationThemeLocations map[string]bool

func (locations navigationThemeLocations) SupportsNavigationLocation(location string) bool {
	return locations[location]
}

func TestResolvePublicNavigationUsesOnlyStoredPlacements(t *testing.T) {
	service := NewService(newFakeStore()).WithNavigationDocumentStore(memoryNavigationDocumentStore{document: NavigationDocument{
		Revision: 7,
		Definitions: []NavigationDefinition{{
			SourceKey: "operator.migrated.abc", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkExternal,
			LabelZhCN: "文档", LabelEnUS: "Docs", Href: "https://example.test/docs", OpenInNewTab: true,
		}},
		Placements: []NavigationPlacement{{
			SourceKey: "core.home", Location: NavigationLocationTopbar, Order: 10, Enabled: true, Visibility: NavigationVisibilityPublic,
		}, {
			SourceKey: "operator.migrated.abc", Location: NavigationLocationTopbar, Order: 20, Enabled: true, Visibility: NavigationVisibilityPublic,
		}, {
			SourceKey: "core.tags", Location: NavigationLocationTopbar, Order: 30, Enabled: false, Visibility: NavigationVisibilityPublic,
		}},
	}})

	resolved, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "en-US", []string{NavigationLocationTopbar, NavigationLocationFooter})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Revision != 7 || len(resolved.Locations) != 2 {
		t.Fatalf("resolved=%#v", resolved)
	}
	topbar := resolved.Locations[0].Items
	if len(topbar) != 2 || topbar[0].SourceKey != "core.home" || topbar[1].SourceKey != "operator.migrated.abc" || topbar[1].Label != "Docs" {
		t.Fatalf("migrated/disabled topbar=%#v", topbar)
	}
	if len(resolved.Locations[1].Items) != 0 {
		t.Fatalf("unstored footer items=%#v", resolved.Locations[1])
	}
}

func TestResolvePublicNavigationReportsActiveThemeCapabilityWithoutDroppingConfiguration(t *testing.T) {
	service := NewService(newFakeStore()).
		WithNavigationDocumentStore(memoryNavigationDocumentStore{document: NavigationDocument{
			Revision: 1,
			Placements: []NavigationPlacement{{
				SourceKey: "core.terms", Location: NavigationLocationFooter, Order: 10, Enabled: true, Visibility: NavigationVisibilityPublic,
			}},
		}}).
		WithNavigationThemeLocations(navigationThemeLocations{NavigationLocationTopbar: true})
	resolved, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "en-US", []string{NavigationLocationTopbar, NavigationLocationFooter})
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Locations[0].Supported || resolved.Locations[1].Supported || len(resolved.Locations[1].Items) == 0 {
		t.Fatalf("theme capability result=%#v", resolved)
	}
}

func TestResolvePublicNavigationFiltersActorVisibilityUnsafeAndMissingExtension(t *testing.T) {
	service := NewService(newFakeStore()).WithNavigationDocumentStore(memoryNavigationDocumentStore{document: NavigationDocument{
		Revision: 3,
		Definitions: []NavigationDefinition{
			{SourceKey: "operator.auth", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "登录后", LabelEnUS: "Members", Href: "/members"},
			{SourceKey: "operator.secret", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "审核", LabelEnUS: "Review", Href: "/review"},
			{SourceKey: "operator.unsafe", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkExternal, LabelZhCN: "坏", LabelEnUS: "Bad", Href: "javascript:alert(1)"},
			{SourceKey: "extension.missing.menu", SourceKind: NavigationSourceExtension, LinkKind: NavigationLinkExtensionRoute, LabelZhCN: "缺失", LabelEnUS: "Missing", ExtensionID: "missing.extension", ContributionID: "menu"},
		},
		Placements: []NavigationPlacement{
			{SourceKey: "operator.auth", Location: NavigationLocationTopbar, Order: 1, Enabled: true, Visibility: NavigationVisibilityAuthenticated},
			{SourceKey: "operator.secret", Location: NavigationLocationTopbar, Order: 2, Enabled: true, Visibility: NavigationVisibilityPermission, Permission: "forum.review"},
			{SourceKey: "operator.unsafe", Location: NavigationLocationTopbar, Order: 3, Enabled: true, Visibility: NavigationVisibilityPublic},
			{SourceKey: "extension.missing.menu", Location: NavigationLocationTopbar, Order: 4, Enabled: true, Visibility: NavigationVisibilityPublic},
		},
	}})

	guest, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "zh-CN", []string{NavigationLocationTopbar})
	if err != nil {
		t.Fatal(err)
	}
	if hasResolvedSource(guest.Locations[0].Items, "operator.auth") || hasResolvedSource(guest.Locations[0].Items, "operator.secret") || hasResolvedSource(guest.Locations[0].Items, "operator.unsafe") || hasResolvedSource(guest.Locations[0].Items, "extension.missing.menu") {
		t.Fatalf("guest output leaked protected or unsafe source: %#v", guest)
	}
	member, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{ID: 8, Status: identity.UserStatusActive, Permissions: map[string]bool{"forum.review": true}}, "en-US", []string{NavigationLocationTopbar})
	if err != nil {
		t.Fatal(err)
	}
	if !hasResolvedSource(member.Locations[0].Items, "operator.auth") || !hasResolvedSource(member.Locations[0].Items, "operator.secret") {
		t.Fatalf("member output=%#v", member)
	}
}

func TestValidateNavigationDefinitionRejectsReservedAndUnsafeTargets(t *testing.T) {
	valid := NavigationDefinition{SourceKey: "operator.docs", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "文档", Href: "/docs"}
	if !ValidateNavigationDefinition(valid) {
		t.Fatal("safe internal definition rejected")
	}
	for _, definition := range []NavigationDefinition{
		{SourceKey: "core.forged", SourceKind: NavigationSourceCore, LinkKind: NavigationLinkCoreRoute, LabelZhCN: "伪造", Href: "/forged"},
		{SourceKey: "not-operator.docs", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "伪造", Href: "/docs"},
		{SourceKey: "operator.admin", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "管理", Href: "/admin/users"},
		{SourceKey: "operator.api", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkInternal, LabelZhCN: "接口", Href: "/api/v1/users"},
		{SourceKey: "operator.script", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkExternal, LabelZhCN: "坏", Href: "javascript:alert(1)"},
		{SourceKey: "operator.credentials", SourceKind: NavigationSourceOperator, LinkKind: NavigationLinkExternal, LabelZhCN: "坏", Href: "https://user:pass@example.test"},
	} {
		if ValidateNavigationDefinition(definition) {
			t.Fatalf("unsafe definition accepted: %#v", definition)
		}
	}
}

func TestResolvePublicNavigationUsesExactRegistryArtifactAndSafeMode(t *testing.T) {
	service := NewService(newFakeStore()).WithNavigationDocumentStore(memoryNavigationDocumentStore{document: NavigationDocument{Revision: 2}})
	registry := navigationregistry.New()
	service.WithNavigationRegistry(registry)
	runtime := &siteChromeRuntime{}
	service.WithNavigationRuntime(runtime, runtime)
	plugin := siteChromePublication("plugin.navigation.public", 'a')
	plugin.Navigation = []navigationregistry.NavigationDeclaration{{
		ID: "plugin.navigation.public.docs", ContractVersion: "plugin.navigation.public.docs@1", Kind: navigationregistry.NavigationKindItem,
		Action: navigationregistry.ActionAdd, TargetID: navigationregistry.CorePrimaryMenuID, Label: "Docs",
		Labels: map[string]string{"en-US": "Docs", "zh-CN": "文档"}, Href: "/docs", Order: 50,
	}}
	if _, err := registry.Publish(plugin); err != nil {
		t.Fatal(err)
	}
	resolved, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "en-US", []string{NavigationLocationTopbar})
	if err != nil || !hasResolvedSource(resolved.Locations[0].Items, "plugin.navigation.public.docs") {
		t.Fatalf("exact artifact resolver=%#v err=%v", resolved, err)
	}
	if _, err := registry.ReplaceAllWithSafeMode([]navigationregistry.Publication{navigationregistry.CorePublication()}, true); err != nil {
		t.Fatal(err)
	}
	safe, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "en-US", []string{NavigationLocationTopbar})
	if err != nil || hasResolvedSource(safe.Locations[0].Items, "plugin.navigation.public.docs") {
		t.Fatalf("safe mode resolver=%#v err=%v", safe, err)
	}
}

func TestResolvePublicNavigationPreservesDocumentOrderThroughRegistryComposition(t *testing.T) {
	service := NewService(newFakeStore()).WithNavigationDocumentStore(memoryNavigationDocumentStore{document: NavigationDocument{
		Revision: 4,
		Placements: []NavigationPlacement{
			{SourceKey: "core.home", Location: NavigationLocationTopbar, Order: 10, Enabled: true, Visibility: NavigationVisibilityPublic},
			{SourceKey: "core.tags", Location: NavigationLocationTopbar, Order: 20, Enabled: true, Visibility: NavigationVisibilityPublic},
			{SourceKey: "core.categories", Location: NavigationLocationTopbar, Order: 30, Enabled: true, Visibility: NavigationVisibilityPublic},
		},
	}})
	service.WithNavigationRegistry(navigationregistry.New())

	resolved, err := service.ResolvePublicNavigation(t.Context(), identity.Actor{}, "zh-CN", []string{NavigationLocationTopbar})
	if err != nil {
		t.Fatal(err)
	}
	items := resolved.Locations[0].Items
	if len(items) != 3 || items[0].SourceKey != "core.home" || items[1].SourceKey != "core.tags" || items[2].SourceKey != "core.categories" {
		t.Fatalf("registry changed document order: %#v", items)
	}
}

func hasResolvedSource(items []ResolvedNavigationItem, sourceKey string) bool {
	for _, item := range items {
		if item.SourceKey == sourceKey {
			return true
		}
	}
	return false
}
