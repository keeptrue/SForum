package providers

import (
	"context"
	"encoding/json"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ExtensionTopicSurfaceProvider 解析 forum.topic.sidebar / badges / list.badges（E2.1 + E2.4）。
type ExtensionTopicSurfaceProvider struct {
	source EffectiveContributionSource
}

func NewExtensionTopicSurfaceProvider(source EffectiveContributionSource) ExtensionTopicSurfaceProvider {
	return ExtensionTopicSurfaceProvider{source: source}
}

func (p ExtensionTopicSurfaceProvider) TopicExtensionSidebar(ctx context.Context) ([]forum.TopicExtensionSidebarItem, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]forum.TopicExtensionSidebarItem, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointForumTopicSidebar {
			continue
		}
		item, ok := topicSidebarFromContribution(contribution)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func (p ExtensionTopicSurfaceProvider) TopicExtensionBadges(ctx context.Context) ([]forum.TopicExtensionBadge, error) {
	return p.topicBadgesForPoint(ctx, extensionmanifest.PointForumTopicBadges)
}

// TopicExtensionListBadges 解析 forum.topic.list.badges（E2.4）；列表级一次返回。
func (p ExtensionTopicSurfaceProvider) TopicExtensionListBadges(ctx context.Context) ([]forum.TopicExtensionBadge, error) {
	return p.topicBadgesForPoint(ctx, extensionmanifest.PointForumTopicListBadges)
}

func (p ExtensionTopicSurfaceProvider) topicBadgesForPoint(ctx context.Context, point string) ([]forum.TopicExtensionBadge, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]forum.TopicExtensionBadge, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != point {
			continue
		}
		item, ok := topicBadgeFromContribution(contribution)
		if !ok {
			continue
		}
		items = append(items, item)
	}
	return items, nil
}

func topicSidebarFromContribution(contribution extensions.EffectiveContribution) (forum.TopicExtensionSidebarItem, bool) {
	var payload extensionmanifest.TopicSidebarContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return forum.TopicExtensionSidebarItem{}, false
	}
	kind := strings.TrimSpace(payload.Type)
	switch kind {
	case extensionmanifest.PayloadTypeExtensionRoute:
		method := strings.ToUpper(strings.TrimSpace(payload.Method))
		path := strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
		if !safeContributionProxyPath(path) {
			return forum.TopicExtensionSidebarItem{}, false
		}
		switch method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return forum.TopicExtensionSidebarItem{}, false
		}
		return forum.TopicExtensionSidebarItem{
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
		if !safePublicHostLink(href) {
			return forum.TopicExtensionSidebarItem{}, false
		}
		return forum.TopicExtensionSidebarItem{
			ExtensionID: contribution.ExtensionID,
			ID:          contribution.ID,
			Order:       contribution.Order,
			Label:       copyContributionLabel(contribution.Label),
			Icon:        contribution.Icon,
			Kind:        "hostLink",
			URL:         href,
		}, true
	default:
		return forum.TopicExtensionSidebarItem{}, false
	}
}

func topicBadgeFromContribution(contribution extensions.EffectiveContribution) (forum.TopicExtensionBadge, bool) {
	var payload extensionmanifest.TopicBadgeContributionPayload
	if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
		return forum.TopicExtensionBadge{}, false
	}
	tone := strings.TrimSpace(payload.Tone)
	switch tone {
	case "neutral", "info", "success", "warning", "danger":
	default:
		return forum.TopicExtensionBadge{}, false
	}
	href := strings.TrimSpace(strings.ReplaceAll(payload.Href, "\\", "/"))
	if href != "" && !safePublicHostLink(href) {
		return forum.TopicExtensionBadge{}, false
	}
	return forum.TopicExtensionBadge{
		ExtensionID: contribution.ExtensionID,
		ID:          contribution.ID,
		Order:       contribution.Order,
		Label:       copyContributionLabel(contribution.Label),
		Tone:        tone,
		Href:        href,
	}, true
}

// safePublicHostLink 与 profile hostLink 一致：站内相对路径，禁止 // 与 /api。
func safePublicHostLink(href string) bool {
	if href == "" || !strings.HasPrefix(href, "/") || strings.HasPrefix(href, "//") {
		return false
	}
	if strings.Contains(href, "://") || strings.Contains(href, "..") {
		return false
	}
	return href != "/api" && !strings.HasPrefix(href, "/api/")
}
