package sitechrome

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

type navigationRegistryConfig struct {
	registry  *navigationregistry.Registry
	admission navigationregistry.RuntimeAdmission
	renderer  navigationregistry.RuntimeRenderer
	traces    *navigationregistry.TraceRing
	composer  *navigationregistry.Composer
	err       error
}

// WithNavigationRegistry enables the V3 composition path and publishes only
// stable neutral Host targets. Existing public list methods remain unchanged.
func (s *Service) WithNavigationRegistry(registry *navigationregistry.Registry) *Service {
	if s == nil {
		return s
	}
	if registry == nil {
		s.navigation = nil
		return s
	}
	config := &navigationRegistryConfig{registry: registry, traces: navigationregistry.NewTraceRing(256)}
	_, config.err = registry.Publish(navigationregistry.CorePublication())
	config.composer = navigationregistry.NewComposer(registry, nil, nil, config.traces)
	s.navigation = config
	return s
}

// WithNavigationRuntime binds exact lifecycle/runtime admission and optional
// handler rendering. A nil runtime keeps every non-Core contribution
// unavailable, which is the safe default.
func (s *Service) WithNavigationRuntime(
	admission navigationregistry.RuntimeAdmission,
	renderer navigationregistry.RuntimeRenderer,
) *Service {
	if s == nil || s.navigation == nil {
		return s
	}
	s.navigation.admission = admission
	s.navigation.renderer = renderer
	s.navigation.composer = navigationregistry.NewComposer(
		s.navigation.registry, admission, renderer, s.navigation.traces,
	)
	return s
}

func (s *Service) NavigationInspector() *navigationregistry.Inspector {
	if s == nil || s.navigation == nil {
		return nil
	}
	return navigationregistry.NewInspector(s.navigation.registry, s.navigation.traces)
}

func (s *Service) ComposePublicNavigation(
	ctx context.Context,
	actor identity.Actor,
	locale string,
) (NavigationRegionViewModel, error) {
	return s.ComposeNavigation(ctx, NavigationCompositionInput{Actor: actor, Locale: locale})
}

// ComposeNavigation is the server-authoritative visibility and composition
// boundary. It passes only actor class plus effective permission keys to plugin
// code; actor IDs and session material never enter cache keys or runtime input.
func (s *Service) ComposeNavigation(
	ctx context.Context,
	input NavigationCompositionInput,
) (NavigationRegionViewModel, error) {
	if s == nil || s.store == nil || ctx == nil {
		return NavigationRegionViewModel{}, ErrInvalid
	}
	items, err := s.store.ListNavItems(ctx, true)
	if err != nil {
		return NavigationRegionViewModel{}, err
	}
	base := make([]navigationregistry.ComposedItem, 0, len(items))
	locale := siteChromeLocale(input.Locale)
	core := navigationregistry.CorePublication().Artifact
	for _, item := range items {
		id := "core.navigation.site.item." + strconv.FormatInt(item.ID, 10)
		attributes := map[string]string(nil)
		if item.OpenInNewTab {
			attributes = map[string]string{"open-in-new-tab": "true"}
		}
		base = append(base, navigationregistry.ComposedItem{
			ID: id, ContractVersion: id + "@1", ProviderID: id, ProviderContractVersion: id + "@1",
			Kind: navigationregistry.NavigationKindItem, Order: item.Position,
			Label: localizedNavItemLabel(item, locale), Href: item.Href, Attributes: attributes, Artifact: core,
		})
	}
	return s.composeNavigationBase(ctx, input, base)
}

func (s *Service) composeNavigationBase(
	ctx context.Context,
	input NavigationCompositionInput,
	base []navigationregistry.ComposedItem,
) (NavigationRegionViewModel, error) {
	locale := siteChromeLocale(input.Locale)
	if s.navigation == nil {
		return defaultNavigationViewModelFromBase(base, locale), nil
	}
	if s.navigation.err != nil || s.navigation.composer == nil {
		return NavigationRegionViewModel{}, fmt.Errorf("site_chrome: configure navigation registry: %w", s.navigation.err)
	}

	visibility := navigationregistry.VisibilityInput{
		Authenticated: input.Actor.ID > 0 && input.Actor.IsActive(),
		Permissions:   effectiveNavigationPermissions(input.Actor, s.navigation.registry.Snapshot()),
		HiddenIDs:     input.HiddenIDs, DisabledProviders: input.DisabledProviders,
	}
	composition, err := s.navigation.composer.Compose(ctx, navigationregistry.CompositionRequest{
		Locale: locale, Visibility: visibility,
		BaseNavigationChildren: map[string][]navigationregistry.ComposedItem{
			navigationregistry.CorePrimaryMenuID: base,
		},
	})
	if err != nil {
		return NavigationRegionViewModel{}, err
	}
	return navigationViewModel(composition), nil
}

func effectiveNavigationPermissions(actor identity.Actor, snapshot navigationregistry.Snapshot) []string {
	set := map[string]bool{}
	for _, contribution := range snapshot.Navigation {
		if contribution.Permission != "" && actor.Can(contribution.Permission) {
			set[contribution.Permission] = true
		}
	}
	for _, contribution := range snapshot.Regions {
		if contribution.Permission != "" && actor.Can(contribution.Permission) {
			set[contribution.Permission] = true
		}
	}
	result := make([]string, 0, len(set))
	for permission := range set {
		result = append(result, permission)
	}
	sort.Strings(result)
	return result
}

func navigationViewModel(composition navigationregistry.Composition) NavigationRegionViewModel {
	result := NavigationRegionViewModel{
		SchemaVersion: NavigationViewModelSchemaVersion, Revision: composition.Revision,
		Digest: composition.Digest, SafeMode: composition.SafeMode, Locale: composition.Locale,
		CacheKey: composition.CacheKey,
		Menus:    []ChromeNodeViewModel{}, Breadcrumbs: []ChromeNodeViewModel{}, Headers: []ChromeNodeViewModel{},
		Footers: []ChromeNodeViewModel{}, Sidebars: []ChromeNodeViewModel{},
		Regions: RegionViewModels{
			Menus: []ChromeNodeViewModel{}, Headers: []ChromeNodeViewModel{}, Footers: []ChromeNodeViewModel{},
			Sidebars: []ChromeNodeViewModel{}, Widgets: []ChromeNodeViewModel{}, Content: []ChromeNodeViewModel{},
		},
	}
	for _, item := range composition.Navigation {
		node := chromeNodeViewModel(item)
		switch item.Kind {
		case navigationregistry.NavigationKindMenu:
			result.Menus = append(result.Menus, node)
		case navigationregistry.NavigationKindBreadcrumb:
			result.Breadcrumbs = append(result.Breadcrumbs, node)
		case navigationregistry.NavigationKindHeader:
			result.Headers = append(result.Headers, node)
		case navigationregistry.NavigationKindFooter:
			result.Footers = append(result.Footers, node)
		case navigationregistry.NavigationKindSidebar:
			result.Sidebars = append(result.Sidebars, node)
		}
	}
	for _, item := range composition.Regions {
		node := chromeNodeViewModel(item)
		switch item.Kind {
		case navigationregistry.RegionKindMenu:
			result.Regions.Menus = append(result.Regions.Menus, node)
		case navigationregistry.RegionKindHeader:
			result.Regions.Headers = append(result.Regions.Headers, node)
		case navigationregistry.RegionKindFooter:
			result.Regions.Footers = append(result.Regions.Footers, node)
		case navigationregistry.RegionKindSidebar:
			result.Regions.Sidebars = append(result.Regions.Sidebars, node)
		case navigationregistry.RegionKindWidget:
			result.Regions.Widgets = append(result.Regions.Widgets, node)
		case navigationregistry.RegionKindContent:
			result.Regions.Content = append(result.Regions.Content, node)
		}
	}
	return result
}

func chromeNodeViewModel(item navigationregistry.ComposedItem) ChromeNodeViewModel {
	result := ChromeNodeViewModel{
		ID: item.ID, Kind: item.Kind, Label: item.Label, Href: item.Href, Content: item.Content,
		Multiple: item.Multiple, Attributes: cloneChromeAttributes(item.Attributes),
	}
	for _, wrapper := range item.Wrappers {
		result.Wrappers = append(result.Wrappers, chromeNodeViewModel(wrapper))
	}
	for _, child := range item.Children {
		result.Children = append(result.Children, chromeNodeViewModel(child))
	}
	return result
}

func cloneChromeAttributes(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func defaultNavigationViewModel(items []NavItem, locale string) NavigationRegionViewModel {
	base := make([]navigationregistry.ComposedItem, 0, len(items))
	for _, item := range items {
		attributes := map[string]string(nil)
		if item.OpenInNewTab {
			attributes = map[string]string{"open-in-new-tab": "true"}
		}
		base = append(base, navigationregistry.ComposedItem{ID: "core.navigation.site.item." + strconv.FormatInt(item.ID, 10), Kind: navigationregistry.NavigationKindItem, Label: localizedNavItemLabel(item, locale), Href: item.Href, Attributes: attributes})
	}
	return defaultNavigationViewModelFromBase(base, locale)
}

func defaultNavigationViewModelFromBase(base []navigationregistry.ComposedItem, locale string) NavigationRegionViewModel {
	children := make([]ChromeNodeViewModel, 0, len(base))
	for _, item := range base {
		children = append(children, ChromeNodeViewModel{ID: item.ID, Kind: item.Kind, Label: item.Label, Href: item.Href, Attributes: cloneChromeAttributes(item.Attributes)})
	}
	result := NavigationRegionViewModel{
		SchemaVersion: NavigationViewModelSchemaVersion, Locale: locale,
		Menus: []ChromeNodeViewModel{{
			ID: navigationregistry.CorePrimaryMenuID, Kind: navigationregistry.NavigationKindMenu,
			Label: coreMenuLabel(locale), Children: children,
		}},
		Breadcrumbs: []ChromeNodeViewModel{}, Headers: []ChromeNodeViewModel{},
		Footers: []ChromeNodeViewModel{}, Sidebars: []ChromeNodeViewModel{},
		Regions: RegionViewModels{
			Menus: []ChromeNodeViewModel{}, Headers: []ChromeNodeViewModel{}, Footers: []ChromeNodeViewModel{},
			Sidebars: []ChromeNodeViewModel{}, Widgets: []ChromeNodeViewModel{}, Content: []ChromeNodeViewModel{},
		},
	}
	body, _ := json.Marshal(result)
	digest := sha256.Sum256(body)
	result.Digest = hex.EncodeToString(digest[:])
	cache := sha256.Sum256([]byte(NavigationViewModelSchemaVersion + "\x00" + locale + "\x00" + result.Digest))
	result.CacheKey = hex.EncodeToString(cache[:])
	return result
}

func localizedNavItemLabel(item NavItem, locale string) string {
	if locale == "en-US" && strings.TrimSpace(item.LabelEnUS) != "" {
		return item.LabelEnUS
	}
	if strings.TrimSpace(item.LabelZhCN) != "" {
		return item.LabelZhCN
	}
	return item.LabelEnUS
}

func coreMenuLabel(locale string) string {
	if locale == "en-US" {
		return "Primary menu"
	}
	return "主导航"
}

func siteChromeLocale(value string) string {
	value = strings.ToLower(strings.TrimSpace(strings.ReplaceAll(value, "_", "-")))
	if value == "en" || strings.HasPrefix(value, "en-") {
		return "en-US"
	}
	return "zh-CN"
}
