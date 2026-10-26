package providers

import (
	"context"
	"encoding/json"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ExtensionNavItemProvider 解析 forum.nav.items（E2.3）。
// 仅公开路径；admin 与 /api 在校验与解析两侧均拒绝。
type ExtensionNavItemProvider struct {
	source EffectiveContributionSource
}

func NewExtensionNavItemProvider(source EffectiveContributionSource) ExtensionNavItemProvider {
	return ExtensionNavItemProvider{source: source}
}

func (p ExtensionNavItemProvider) ExtensionNavItems(ctx context.Context) ([]sitechrome.ExtensionNavItem, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]sitechrome.ExtensionNavItem, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointForumNavItems {
			continue
		}
		item, ok := navItemFromContribution(contribution)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func navItemFromContribution(contribution extensions.EffectiveContribution) (sitechrome.ExtensionNavItem, bool) {
	var payload extensionmanifest.NavItemContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return sitechrome.ExtensionNavItem{}, false
	}
	kind := strings.TrimSpace(payload.Type)
	switch kind {
	case extensionmanifest.PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		path := strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
		// 公开导航只允许 GET 打开扩展页。
		if method != "GET" || !safeContributionProxyPath(path) {
			return sitechrome.ExtensionNavItem{}, false
		}
		return sitechrome.ExtensionNavItem{
			ExtensionID: contribution.ExtensionID,
			ID:          contribution.ID,
			Order:       contribution.Order,
			Label:       copyContributionLabel(contribution.Label),
			Icon:        contribution.Icon,
			Kind:        "extensionRoute",
			Method:      method,
			URL:         extensionProxyURL(contribution.ExtensionID, path),
		}, true
	case "hostLink":
		href := strings.TrimSpace(strings.ReplaceAll(payload.Href, "\\", "/"))
		if !safePublicNavHostLink(href) {
			return sitechrome.ExtensionNavItem{}, false
		}
		return sitechrome.ExtensionNavItem{
			ExtensionID: contribution.ExtensionID,
			ID:          contribution.ID,
			Order:       contribution.Order,
			Label:       copyContributionLabel(contribution.Label),
			Icon:        contribution.Icon,
			Kind:        "hostLink",
			URL:         href,
		}, true
	default:
		return sitechrome.ExtensionNavItem{}, false
	}
}

// safePublicNavHostLink 与 manifest 校验一致：站内相对路径，禁止 /api 与 /admin。
func safePublicNavHostLink(href string) bool {
	if !safePublicHostLink(href) {
		return false
	}
	return href != "/admin" && !strings.HasPrefix(href, "/admin/")
}
