package extensionscontroller

import (
	"sort"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	pages "github.com/keeptrue/sforum/apps/api/app/Support/Pages"
)

const (
	templateInspectorDefaultLimit = 50
	templateInspectorMaximumLimit = 200
	templateInspectorUnavailable  = "extensions.template_inspector_unavailable"
	templateInspectorInvalid      = "extensions.template_inspector_invalid"
	// templateInspectorSchemaVersion 是 admin 检查器响应契约版本，与 Theme Runtime
	// 内部 binding revision 解耦，便于后续扩展字段。
	templateInspectorSchemaVersion = "sforum.template-inspector@1"
)

// TemplateInspectorSnapshot is the admin-facing redacted Theme Runtime view.
// Package roots, compiled template bodies, and view-model payloads are never
// included; only stable artifact identity and contribution/override keys.
type TemplateInspectorSnapshot struct {
	SchemaVersion string                          `json:"schemaVersion"`
	Revision      uint64                          `json:"revision"`
	ActiveTheme   string                          `json:"activeTheme,omitempty"`
	DefaultTheme  string                          `json:"defaultTheme,omitempty"`
	SnapshotCount int                             `json:"snapshotCount"`
	OverrideCount int                             `json:"overrideCount"`
	Snapshots     []pages.ThemeRuntimeInspectItem `json:"snapshots"`
}

func (h *Controller) inspectTemplates(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.themeRuntime == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, templateInspectorUnavailable)
	}
	limit, err := parseInspectorLimit(c.Query("limit"), templateInspectorDefaultLimit, templateInspectorMaximumLimit)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, templateInspectorInvalid)
	}
	return apphttp.OK(c, buildTemplateInspectorSnapshot(h.themeRuntime.InspectSnapshot(), limit))
}

// buildTemplateInspectorSnapshot 将 Theme Runtime 脱敏检查结果投影为 admin 契约。
// snapshotCount/overrideCount 始终反映全量图；snapshots 按 extensionId 排序后截断。
func buildTemplateInspectorSnapshot(inspection pages.ThemeRuntimeInspection, limit int) TemplateInspectorSnapshot {
	snapshots := append([]pages.ThemeRuntimeInspectItem(nil), inspection.Snapshots...)
	// InspectSnapshot 已排序；此处再保证 limit 截断后顺序稳定。
	sort.SliceStable(snapshots, func(i, j int) bool {
		if snapshots[i].ExtensionID == snapshots[j].ExtensionID {
			return snapshots[i].PackageDigest < snapshots[j].PackageDigest
		}
		return snapshots[i].ExtensionID < snapshots[j].ExtensionID
	})
	if limit > 0 && len(snapshots) > limit {
		snapshots = snapshots[:limit]
	}
	return TemplateInspectorSnapshot{
		SchemaVersion: templateInspectorSchemaVersion,
		Revision:      inspection.Revision,
		ActiveTheme:   inspection.ActiveTheme,
		DefaultTheme:  inspection.DefaultTheme,
		SnapshotCount: inspection.SnapshotCount,
		OverrideCount: inspection.OverrideCount,
		Snapshots:     snapshots,
	}
}
