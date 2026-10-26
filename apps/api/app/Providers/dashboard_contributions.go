package providers

import (
	"context"
	"encoding/json"
	"strings"

	adminoverview "github.com/keeptrue/sforum/apps/api/app/Models/AdminOverview"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
)

// ExtensionDashboardWidgetProvider 解析 admin.dashboard.widgets（F4.3）。
type ExtensionDashboardWidgetProvider struct {
	source EffectiveContributionSource
}

func NewExtensionDashboardWidgetProvider(source EffectiveContributionSource) ExtensionDashboardWidgetProvider {
	return ExtensionDashboardWidgetProvider{source: source}
}

func (p ExtensionDashboardWidgetProvider) DashboardWidgets(ctx context.Context) ([]adminoverview.ExtensionWidget, error) {
	if p.source == nil {
		return nil, nil
	}
	contributions, err := p.source.EffectiveContributions(ctx)
	if err != nil {
		return nil, err
	}
	widgets := make([]adminoverview.ExtensionWidget, 0, len(contributions))
	for _, contribution := range contributions {
		if contribution.Point != extensionmanifest.PointAdminDashboardWidgets {
			continue
		}
		var payload extensionmanifest.DashboardWidgetContributionPayload
		if err := json.Unmarshal(contribution.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Type) != "adminLink" {
			continue
		}
		route := strings.TrimSpace(strings.ReplaceAll(payload.Route, "\\", "/"))
		if route == "" || !strings.HasPrefix(route, "/") || strings.HasPrefix(route, "//") {
			continue
		}
		if strings.Contains(route, "://") || strings.Contains(route, "..") {
			continue
		}
		if route == "/api" || strings.HasPrefix(route, "/api/") {
			continue
		}
		severity := strings.TrimSpace(payload.Severity)
		if severity == "" {
			severity = "info"
		}
		switch severity {
		case "info", "success", "warning", "danger":
		default:
			continue
		}
		widgets = append(widgets, adminoverview.ExtensionWidget{
			ExtensionID: contribution.ExtensionID,
			ID:          contribution.ID,
			Order:       contribution.Order,
			Label:       copyContributionLabel(contribution.Label),
			Icon:        contribution.Icon,
			Route:       route,
			Severity:    severity,
		})
	}
	return widgets, nil
}
