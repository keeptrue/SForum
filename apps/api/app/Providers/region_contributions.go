package providers

import (
	"context"
	"encoding/json"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	regioncatalog "github.com/keeptrue/sforum/apps/api/app/Support/RegionCatalog"
)

// ExtensionPageRegionProvider 解析 forum.page.regions 贡献为宿主区域描述符。
type ExtensionPageRegionProvider struct {
	source EffectiveContributionSource
}

func NewExtensionPageRegionProvider(source EffectiveContributionSource) ExtensionPageRegionProvider {
	return ExtensionPageRegionProvider{source: source}
}

const (
	pageRegionItemKindLink   = "link"
	pageRegionItemKindAction = "action"
	pageRegionItemKindWidget = "widget"
)

// PageRegions 返回 pageID 上每个标准区域的按序内容;无内容的区域省略。
// 目录未收录的 page 返回 nil(调用方据此 404);安全模式下 EffectiveContributions 为空。
func (p ExtensionPageRegionProvider) PageRegions(ctx context.Context, pageID string) ([]sitechrome.PageRegionViewModel, error) {
	regions := regioncatalog.PageRegions(pageID)
	if len(regions) == 0 {
		return nil, nil
	}
	byRegion := map[string][]sitechrome.PageRegionItem{}
	if p.source != nil {
		contributions, err := p.source.EffectiveContributions(ctx)
		if err != nil {
			return nil, err
		}
		for _, contribution := range contributions {
			if contribution.Point != extensionmanifest.PointForumPageRegions {
				continue
			}
			regionID, item, ok := pageRegionItemFromContribution(contribution, pageID)
			if !ok {
				continue
			}
			byRegion[regionID] = append(byRegion[regionID], item)
		}
	}
	result := make([]sitechrome.PageRegionViewModel, 0, len(regions))
	for _, region := range regions {
		items := byRegion[region.ID]
		if len(items) == 0 {
			continue
		}
		result = append(result, sitechrome.PageRegionViewModel{ID: region.ID, Kind: region.Kind, Items: items})
	}
	return result, nil
}

// pageRegionItemFromContribution 把贡献映射为宿主描述符。
// 清单校验已把关;此处对 page×region 白名单与路径再做 fail-closed 复核,防旧包漂移。
func pageRegionItemFromContribution(contribution extensions.EffectiveContribution, pageID string) (string, sitechrome.PageRegionItem, bool) {
	var payload extensionmanifest.RegionPlacementContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return "", sitechrome.PageRegionItem{}, false
	}
	regionID := strings.TrimSpace(payload.Region)
	if !regioncatalog.Valid(pageID, regionID) {
		return "", sitechrome.PageRegionItem{}, false
	}
	matched := false
	for _, page := range payload.Pages {
		if strings.TrimSpace(page) == pageID {
			matched = true
			break
		}
	}
	if !matched {
		return "", sitechrome.PageRegionItem{}, false
	}
	item := sitechrome.PageRegionItem{
		ExtensionID:    contribution.ExtensionID,
		ContributionID: contribution.ID,
		Label:          copyContributionLabel(contribution.Label),
		Icon:           contribution.Icon,
		Order:          contribution.Order,
	}
	switch strings.TrimSpace(payload.Type) {
	case "hostLink":
		href := strings.TrimSpace(strings.ReplaceAll(payload.Href, "\\", "/"))
		if !safePublicHostLink(href) {
			return "", sitechrome.PageRegionItem{}, false
		}
		item.Kind = pageRegionItemKindLink
		item.Href = href
		return regionID, item, true
	case extensionmanifest.PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		path := strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return "", sitechrome.PageRegionItem{}, false
		}
		if !safeContributionProxyPath(path) {
			return "", sitechrome.PageRegionItem{}, false
		}
		item.Kind = pageRegionItemKindAction
		item.Method = method
		item.Path = path
		return regionID, item, true
	case extensionmanifest.PayloadRegionPlacementL2Widget:
		componentID := extensionmanifest.NormalizeID(payload.ComponentID)
		if componentID == "" {
			return "", sitechrome.PageRegionItem{}, false
		}
		item.Kind = pageRegionItemKindWidget
		// 组件永远归属贡献所在扩展;禁止跨包引用。
		item.Widget = &sitechrome.PageRegionWidgetRef{
			ExtensionID: contribution.ExtensionID,
			ComponentID: componentID,
		}
		return regionID, item, true
	default:
		return "", sitechrome.PageRegionItem{}, false
	}
}
