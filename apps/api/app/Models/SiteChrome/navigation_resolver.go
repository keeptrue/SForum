package sitechrome

import (
	"context"
	"net/url"
	"sort"
	"strings"
	"unicode"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

// ResolvePublicNavigation is the canonical, presentation-only navigation
// resolver. It does not cache results: caller-side caches must vary by actor
// class and effective permissions, never reuse a personalized response for an
// anonymous request.
func (s *Service) ResolvePublicNavigation(
	ctx context.Context,
	actor identity.Actor,
	locale string,
	locations []string,
) (ResolvedNavigation, error) {
	if s == nil || s.navigationDocuments == nil || ctx == nil {
		return ResolvedNavigation{}, ErrInvalid
	}
	document, err := s.navigationDocuments.ReadNavigationDocument(ctx)
	if err != nil {
		return ResolvedNavigation{}, err
	}
	if document.Revision < 1 {
		document.Revision = 1
	}
	selected, ok := normalizeNavigationLocations(locations)
	if !ok {
		return ResolvedNavigation{}, ErrInvalid
	}
	definitions := navigationDefinitionsByKey(document.Definitions)
	for _, definition := range CoreNavigationDefinitions() {
		definitions[definition.SourceKey] = definition
	}
	placements := navigationPlacementsByKey(document.Placements)

	result := ResolvedNavigation{SchemaVersion: NavigationDocumentSchemaVersion, Revision: document.Revision, Locations: make([]ResolvedNavigationLocation, 0, len(selected))}
	for _, location := range selected {
		items := make([]resolvedPlacement, 0)
		for _, placement := range placements {
			if placement.Location != location || !placement.Enabled || !visibleNavigationPlacement(placement, actor) {
				continue
			}
			definition, exists := definitions[placement.SourceKey]
			if !exists || !validPublicNavigationDefinition(definition) {
				continue
			}
			items = append(items, resolvedPlacement{placement: placement, item: resolvedNavigationItem(definition, placement, locale)})
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].placement.Order != items[j].placement.Order {
				return items[i].placement.Order < items[j].placement.Order
			}
			return items[i].item.SourceKey < items[j].item.SourceKey
		})
		resolved := resolvedNavigationItems(items)
		if location == NavigationLocationTopbar {
			resolved, err = s.composeResolvedTopbar(ctx, actor, locale, items)
			if err != nil {
				return ResolvedNavigation{}, err
			}
		}
		result.Locations = append(result.Locations, ResolvedNavigationLocation{Location: location, Supported: s.navigationLocationSupported(location), Items: resolved})
	}
	return result, nil
}

func (s *Service) navigationLocationSupported(location string) bool {
	if s == nil || s.navigationTheme == nil {
		// Core fallback owns safe rendering until M6 activates theme-declared
		// capability metadata; unsupported configuration remains readable.
		return true
	}
	return s.navigationTheme.SupportsNavigationLocation(location)
}

func (s *Service) composeResolvedTopbar(
	ctx context.Context,
	actor identity.Actor,
	locale string,
	items []resolvedPlacement,
) ([]ResolvedNavigationItem, error) {
	if s == nil || s.navigation == nil {
		return resolvedNavigationItems(items), nil
	}
	base := make([]navigationregistry.ComposedItem, 0, len(items))
	baseByID := make(map[string]ResolvedNavigationItem, len(items))
	core := navigationregistry.CorePublication().Artifact
	for _, resolved := range items {
		item := resolved.item
		id := "core.navigation.document." + item.SourceKey
		attributes := map[string]string(nil)
		if item.OpenInNewTab {
			attributes = map[string]string{"open-in-new-tab": "true"}
		}
		base = append(base, navigationregistry.ComposedItem{ID: id, ContractVersion: id + "@1", ProviderID: id,
			ProviderContractVersion: id + "@1", Kind: navigationregistry.NavigationKindItem, Label: item.Label,
			Href: item.Href, Order: resolved.placement.Order, Attributes: attributes, Artifact: core})
		baseByID[id] = item
	}
	view, err := s.composeNavigationBase(ctx, NavigationCompositionInput{Actor: actor, Locale: locale}, base)
	if err != nil {
		return nil, err
	}
	for _, menu := range view.Menus {
		if menu.ID != navigationregistry.CorePrimaryMenuID {
			continue
		}
		result := make([]ResolvedNavigationItem, 0, len(menu.Children))
		for _, node := range menu.Children {
			if item, known := baseByID[node.ID]; known {
				item.Label, item.Href = node.Label, node.Href
				item.OpenInNewTab = node.Attributes != nil && node.Attributes["open-in-new-tab"] == "true"
				result = append(result, item)
				continue
			}
			if node.Kind != navigationregistry.NavigationKindItem || node.Href == "" || !safePublicNavigationHref(node.Href) {
				continue
			}
			linkKind := NavigationLinkExtensionHost
			if strings.HasPrefix(node.Href, "/extensions/") {
				linkKind = NavigationLinkExtensionRoute
			}
			result = append(result, ResolvedNavigationItem{SourceKey: node.ID, SourceKind: NavigationSourceExtension,
				LinkKind: linkKind, Label: node.Label, Href: node.Href,
				OpenInNewTab: node.Attributes != nil && node.Attributes["open-in-new-tab"] == "true"})
		}
		return result, nil
	}
	return resolvedNavigationItems(items), nil
}

type resolvedPlacement struct {
	placement NavigationPlacement
	item      ResolvedNavigationItem
}

func resolvedNavigationItems(items []resolvedPlacement) []ResolvedNavigationItem {
	result := make([]ResolvedNavigationItem, 0, len(items))
	for _, item := range items {
		result = append(result, item.item)
	}
	return result
}

func navigationDefinitionsByKey(input []NavigationDefinition) map[string]NavigationDefinition {
	result := make(map[string]NavigationDefinition, len(input)+8)
	for _, definition := range input {
		if definition.SourceKey != "" {
			result[definition.SourceKey] = definition
		}
	}
	return result
}

func navigationPlacementsByKey(input []NavigationPlacement) map[string]NavigationPlacement {
	result := make(map[string]NavigationPlacement, len(input)+16)
	for _, placement := range input {
		if placement.SourceKey != "" && placement.Location != "" {
			result[navigationPlacementKey(placement.SourceKey, placement.Location)] = placement
		}
	}
	return result
}

func navigationPlacementKey(sourceKey, location string) string { return sourceKey + "\x00" + location }

func normalizeNavigationLocations(input []string) ([]string, bool) {
	if len(input) == 0 {
		return NavigationLocations(), true
	}
	known := map[string]bool{}
	for _, location := range NavigationLocations() {
		known[location] = true
	}
	result := make([]string, 0, len(input))
	seen := map[string]bool{}
	for _, location := range input {
		location = strings.TrimSpace(location)
		if !known[location] || seen[location] {
			return nil, false
		}
		seen[location] = true
		result = append(result, location)
	}
	return result, true
}

func visibleNavigationPlacement(placement NavigationPlacement, actor identity.Actor) bool {
	switch placement.Visibility {
	case NavigationVisibilityPublic:
		return true
	case NavigationVisibilityAnonymous:
		return actor.ID <= 0 || !actor.IsActive()
	case NavigationVisibilityAuthenticated:
		return actor.ID > 0 && actor.IsActive()
	case NavigationVisibilityPermission:
		return placement.Permission != "" && actor.ID > 0 && actor.IsActive() && actor.Can(placement.Permission)
	default:
		return false
	}
}

func resolvedNavigationItem(definition NavigationDefinition, placement NavigationPlacement, locale string) ResolvedNavigationItem {
	labelZh, labelEn, icon := definition.LabelZhCN, definition.LabelEnUS, definition.Icon
	if placement.LabelZhCN != "" {
		labelZh = placement.LabelZhCN
	}
	if placement.LabelEnUS != "" {
		labelEn = placement.LabelEnUS
	}
	if placement.IconHidden {
		icon = ""
	} else if placement.Icon != "" {
		icon = placement.Icon
	}
	label := labelZh
	if siteChromeLocale(locale) == "en-US" && labelEn != "" {
		label = labelEn
	}
	if label == "" {
		label = labelEn
	}
	return ResolvedNavigationItem{SourceKey: definition.SourceKey, SourceKind: definition.SourceKind, LinkKind: definition.LinkKind,
		Label: label, Href: definition.Href, Icon: icon, IconHidden: placement.IconHidden, MaxItems: placement.MaxItems, OpenInNewTab: definition.OpenInNewTab}
}

func validPublicNavigationDefinition(definition NavigationDefinition) bool {
	return ValidateNavigationDefinition(definition)
}

// ValidateNavigationDefinition is the typed target boundary shared by the
// resolver now and M2's transactional command handler later. It deliberately
// has no raw HTML, script, arbitrary route, or database reference escape hatch.
func ValidateNavigationDefinition(definition NavigationDefinition) bool {
	if definition.SourceKey == "" || len([]rune(definition.SourceKey)) > 160 ||
		len([]rune(definition.LabelZhCN)) > NavigationMaxLabelRunes || len([]rune(definition.LabelEnUS)) > NavigationMaxLabelRunes ||
		len([]rune(definition.Icon)) > NavigationMaxIconRunes || containsUnsafeNavigationText(definition.SourceKey) ||
		containsUnsafeNavigationText(definition.LabelZhCN) || containsUnsafeNavigationText(definition.LabelEnUS) || containsUnsafeNavigationText(definition.Icon) {
		return false
	}
	switch definition.SourceKind {
	case NavigationSourceCore:
		return isCoreNavigationDefinition(definition)
	case NavigationSourceDynamic:
		return definition.LinkKind == NavigationLinkDynamicBlock && definition.Href == "" && isCoreNavigationDefinition(definition)
	case NavigationSourceOperator:
		if !strings.HasPrefix(definition.SourceKey, "operator.") {
			return false
		}
	case NavigationSourceExtension:
		// Exact-artifact extension output is composed from NavigationRegistry;
		// persisted references remain inert until that registry proves them.
		return false
	default:
		return false
	}
	switch definition.LinkKind {
	case NavigationLinkCoreRoute, NavigationLinkInternal:
		return safeInternalNavigationHref(definition.Href)
	case NavigationLinkExternal:
		return safeExternalNavigationHref(definition.Href)
	case NavigationLinkDynamicBlock:
		return definition.SourceKind == NavigationSourceDynamic && definition.Href == ""
	case NavigationLinkExtensionHost, NavigationLinkExtensionRoute:
		// M1 persists extension references but only lifecycle-published exact
		// artifacts may become public items. M5 consumes them through the registry.
		return false
	default:
		return false
	}
}

func isCoreNavigationDefinition(definition NavigationDefinition) bool {
	for _, core := range CoreNavigationDefinitions() {
		if definition.SourceKey == core.SourceKey && definition.SourceKind == core.SourceKind &&
			definition.LinkKind == core.LinkKind && definition.Href == core.Href {
			return true
		}
	}
	return false
}

func safePublicNavigationHref(value string) bool {
	return safeInternalNavigationHref(value) || safeExternalNavigationHref(value)
}

func safeExternalNavigationHref(value string) bool {
	if containsUnsafeNavigationText(value) || len([]rune(value)) > hrefMaxRunes {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") && parsed.Host != "" && parsed.User == nil
}

func safeInternalNavigationHref(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") || strings.HasPrefix(value, "//") || strings.HasPrefix(value, "/api") || strings.HasPrefix(value, "/admin") {
		return false
	}
	return !containsUnsafeNavigationText(value)
}

func containsUnsafeNavigationText(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	lower := strings.ToLower(value)
	return strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:text/html") || strings.Contains(lower, "<script")
}
