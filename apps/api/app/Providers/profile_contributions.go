package providers

import (
	"context"
	"encoding/json"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ExtensionProfileTabProvider 解析 forum.profile.tabs（F4.3）。
type ExtensionProfileTabProvider struct {
	source EffectiveContributionSource
}

func NewExtensionProfileTabProvider(source EffectiveContributionSource) ExtensionProfileTabProvider {
	return ExtensionProfileTabProvider{source: source}
}

func (p ExtensionProfileTabProvider) ProfileTabs(ctx context.Context) ([]profile.ProfileExtensionTab, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	tabs := make([]profile.ProfileExtensionTab, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointForumProfileTabs {
			continue
		}
		tab, ok := profileTabFromContribution(contribution)
		if !ok {
			continue
		}
		tabs = append(tabs, tab)
	}
	return tabs, nil
}

func profileTabFromContribution(contribution extensions.EffectiveContribution) (profile.ProfileExtensionTab, bool) {
	var payload extensionmanifest.ProfileTabContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return profile.ProfileExtensionTab{}, false
	}
	kind := strings.TrimSpace(payload.Type)
	switch kind {
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
		return profile.ProfileExtensionTab{
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
		if href == "" || !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
			return profile.ProfileExtensionTab{}, false
		}
		if strings.Contains(href, "://") || strings.Contains(href, "..") {
			return profile.ProfileExtensionTab{}, false
		}
		if href == "/api" || strings.HasPrefix(href, "/api/") {
			return profile.ProfileExtensionTab{}, false
		}
		return profile.ProfileExtensionTab{
			ExtensionID: contribution.ExtensionID,
			ID:          contribution.ID,
			Order:       contribution.Order,
			Label:       copyContributionLabel(contribution.Label),
			Icon:        contribution.Icon,
			Kind:        "hostLink",
			URL:         href,
		}, true
	default:
		return profile.ProfileExtensionTab{}, false
	}
}
