package sitechrome

import (
	"context"
	"sort"
	"strings"

	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	navigationregistry "github.com/keeptrue/sforum/apps/api/app/Support/NavigationRegistry"
)

// AccountSettingsNavItem is the redacted Host DTO used by the personal
// settings shell. Exact package/runtime digests stay in the permissioned
// registry inspector and are never exposed to the browser.
type AccountSettingsNavItem struct {
	ID          string `json:"id"`
	ExtensionID string `json:"extensionId,omitempty"`
	Label       string `json:"label"`
	Href        string `json:"href"`
	Order       int    `json:"order"`
	Icon        string `json:"icon,omitempty"`
}

// ResolveAccountSettingsNavigation resolves only the Host-owned account
// settings surface. It is intentionally separate from public site chrome.
func (s *Service) ResolveAccountSettingsNavigation(ctx context.Context, actor identity.Actor, locale string) ([]AccountSettingsNavItem, error) {
	if s == nil || s.navigation == nil || s.navigation.composer == nil || ctx == nil || !actor.IsActive() {
		return []AccountSettingsNavItem{}, nil
	}
	// 与 composeNavigationBase 一致：先归一化 Accept-Language 再进 Composer，
	// 否则带 q 值的原始头会让 NavigationRegistry.normalizeLocale 判 ErrInvalid。
	locale = siteChromeLocale(locale)
	owned := []string(nil)
	if s.accountResources != nil {
		var err error
		owned, err = s.accountResources.OwnedResourceKeys(ctx, actor)
		if err != nil {
			return nil, err
		}
	}
	permissions := effectiveNavigationPermissions(actor, s.navigation.registry.Snapshot())
	composition, err := s.navigation.composer.Compose(ctx, navigationregistry.CompositionRequest{
		Locale:          locale,
		NavigationKinds: []string{navigationregistry.NavigationKindAccountSettings},
		Visibility: navigationregistry.VisibilityInput{
			Authenticated: true, Permissions: permissions, OwnedResources: owned,
		},
	})
	if err != nil {
		return nil, err
	}
	items := make([]AccountSettingsNavItem, 0)
	for _, node := range composition.Navigation {
		if node.Kind != navigationregistry.NavigationKindAccountSettings || !safeAccountSettingsHref(node.Href) {
			continue
		}
		items = append(items, AccountSettingsNavItem{
			ID: node.ID, ExtensionID: node.Artifact.ExtensionID, Label: strings.TrimSpace(node.Label),
			Href: node.Href, Order: node.Order,
		})
		if len(items) >= 128 {
			break
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].Order != items[j].Order {
			return items[i].Order < items[j].Order
		}
		return items[i].ID < items[j].ID
	})
	return items, nil
}

func safeAccountSettingsHref(href string) bool {
	href = strings.TrimSpace(strings.ReplaceAll(href, "\\", "/"))
	return strings.HasPrefix(href, "/settings/") || strings.HasPrefix(href, "/extensions/")
}
