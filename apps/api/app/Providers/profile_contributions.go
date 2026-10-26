package providers

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// profileTabKindWidget 是 tab 面板内挂载公开 L2 组件的 tab 形态。
const profileTabKindWidget = "widget"

// maxProfileWidgetTabs 是单次资料页渲染允许挂载的 widget tab 上限。
// 每个 widget 都是一次 L2 挂载 + CSP 聚合，链接 tab 不受此限。
const maxProfileWidgetTabs = 8

// ExtensionProfileTabProvider 解析 forum.profile.tabs（F4.3）。
type ExtensionProfileTabProvider struct {
	source EffectiveContributionSource
}

func NewExtensionProfileTabProvider(source EffectiveContributionSource) ExtensionProfileTabProvider {
	return ExtensionProfileTabProvider{source: source}
}

// ProfileTabs 按 viewer 过滤可见 tab：permission 为空的全员可见，
// 非空的只对 Can(permission) 为真的访客出现。过滤必须发生在服务端，
// 因为 Permission 字段本身永不下发前端。
func (p ExtensionProfileTabProvider) ProfileTabs(ctx context.Context, viewer identity.Actor) ([]profile.ProfileExtensionTab, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	tabs := make([]profile.ProfileExtensionTab, 0, len(contributions))
	widgets := 0
	dropped := 0
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointForumProfileTabs {
			continue
		}
		tab, ok := profileTabFromContribution(contribution)
		if !ok {
			continue
		}
		if tab.Permission != "" && !viewer.Can(tab.Permission) {
			continue
		}
		if tab.Kind == profileTabKindWidget {
			if widgets >= maxProfileWidgetTabs {
				dropped++
				continue
			}
			widgets++
		}
		tabs = append(tabs, tab)
	}
	if dropped > 0 {
		slog.Warn("profile: widget tab contributions truncated",
			"limit", maxProfileWidgetTabs, "dropped", dropped)
	}
	return tabs, nil
}

func profileTabFromContribution(contribution extensions.EffectiveContribution) (profile.ProfileExtensionTab, bool) {
	var payload extensionmanifest.ProfileTabContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return profile.ProfileExtensionTab{}, false
	}
	tab := profile.ProfileExtensionTab{
		ExtensionID: contribution.ExtensionID,
		ID:          contribution.ID,
		Order:       contribution.Order,
		Label:       copyContributionLabel(contribution.Label),
		Icon:        contribution.Icon,
		Permission:  strings.TrimSpace(payload.Permission),
	}
	switch strings.TrimSpace(payload.Type) {
	case extensionmanifest.PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		path := strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
		if !safeContributionProxyPath(path) {
			return profile.ProfileExtensionTab{}, false
		}
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return profile.ProfileExtensionTab{}, false
		}
		tab.Kind = "extensionRoute"
		tab.Method = method
		tab.URL = extensionProxyURL(contribution.ExtensionID, path)
		return tab, true
	case "hostLink":
		href := strings.TrimSpace(strings.ReplaceAll(payload.Href, "\\", "/"))
		if href == "" || !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
			return profile.ProfileExtensionTab{}, false
		}
		if strings.Contains(href, "://") || strings.Contains(href, "..") {
			return profile.ProfileExtensionTab{}, false
		}
		if href == "/api" || strings.HasPrefix(href, "/api/") {
			return profile.ProfileExtensionTab{}, false
		}
		tab.Kind = "hostLink"
		tab.URL = href
		return tab, true
	case extensionmanifest.PayloadProfileTabL2Widget:
		componentID := extensionmanifest.NormalizeID(payload.ComponentID)
		if componentID == "" {
			return profile.ProfileExtensionTab{}, false
		}
		// widget 不走扩展路由代理：URL 保持为空，前端按 ComponentID 交给公开 L2 运行时。
		tab.Kind = profileTabKindWidget
		tab.ComponentID = componentID
		return tab, true
	default:
		return profile.ProfileExtensionTab{}, false
	}
}
