package providers

import (
	"context"
	"encoding/json"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// EffectiveContributionSource 供各模块解析已启用插件的贡献。
type EffectiveContributionSource interface {
	EffectiveContributions(ctx context.Context) ([]extensions.EffectiveContribution, error)
}

func copyContributionLabel(labels map[string]string) map[string]string {
	if len(labels) == 0 {
		return nil
	}
	copied := make(map[string]string, len(labels))
	for locale, label := range labels {
		copied[locale] = label
	}
	return copied
}

func parseExtensionRoutePayload(raw json.RawMessage) (extensionmanifest.TopicActionContributionPayload, bool) {
	var payload extensionmanifest.TopicActionContributionPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return payload, false
	}
	payload.Type = strings.TrimSpace(payload.Type)
	payload.Method = strings.ToUpper(strings.TrimSpace(payload.Method))
	payload.Path = strings.TrimSpace(strings.ReplaceAll(payload.Path, "\\", "/"))
	if payload.Type != extensionmanifest.PayloadTypeExtensionRoute {
		return payload, false
	}
	switch payload.Method {
	case "POST", "PUT", "PATCH", "DELETE", "GET":
	default:
		return payload, false
	}
	if !safeContributionProxyPath(payload.Path) {
		return payload, false
	}
	return payload, true
}

func safeContributionProxyPath(value string) bool {
	if value == "" || !strings.HasPrefix(value, "/") || value == "/" {
		return false
	}
	if strings.Contains(value, "://") || strings.Contains(value, "..") {
		return false
	}
	return value != "/api" && !strings.HasPrefix(value, "/api/")
}

func extensionProxyURL(extensionID, path string) string {
	return "/extensions/" + strings.TrimSpace(extensionID) + path
}
