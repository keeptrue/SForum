package extensionscontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	extensionmanifest "github.com/keeptrue/sforum/apps/api/app/Support/ExtensionManifest"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const routeCatalogUnavailableReason = "extensions.route_catalog_unavailable"

// routeCatalogEntryView 与 routeProviderCandidate / InspectorStep 的字段口径一致，
// 附加目录专属的 admitted 与 conflictKinds，供前端按来源分组渲染。
type routeCatalogEntryView struct {
	RouteID         string                 `json:"routeId"`
	ContractVersion string                 `json:"contractVersion"`
	Action          string                 `json:"action"`
	TargetRouteID   string                 `json:"targetRouteId,omitempty"`
	Method          string                 `json:"method"`
	Path            string                 `json:"path"`
	PathSignature   string                 `json:"pathSignature"`
	Priority        int                    `json:"priority"`
	ProviderKind    routes.ProviderKind    `json:"providerKind"`
	Artifact        *routes.PluginArtifact `json:"artifact,omitempty"`
	Guard           string                 `json:"guard"`
	Permission      string                 `json:"permission,omitempty"`
	Handler         string                 `json:"handler,omitempty"`
	Destination     string                 `json:"destination,omitempty"`
	Mode            string                 `json:"mode"`
	Fallback        string                 `json:"fallback"`
	RequestSchema   string                 `json:"requestSchema,omitempty"`
	ResponseSchema  string                 `json:"responseSchema,omitempty"`
	TimeoutMS       int                    `json:"timeoutMs"`
	GuardRisk       string                 `json:"guardRisk"`
	Admitted        bool                   `json:"admitted"`
	ConflictKinds   []string               `json:"conflictKinds"`
}

type routeCatalogResponse struct {
	Revision uint64                  `json:"revision"`
	SafeMode bool                    `json:"safeMode"`
	Total    int                     `json:"total"`
	Routes   []routeCatalogEntryView `json:"routes"`
}

// routeCatalog 返回当前不可变 Route Registry 快照的全量路由目录（只读观测面）。
func (h *Controller) routeCatalog(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeCatalogAPI == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeCatalogUnavailableReason)
	}
	snapshot, err := h.routeCatalogAPI.Snapshot()
	if err != nil {
		return mapRouteCatalogError(err)
	}
	response := routeCatalogResponse{
		Revision: snapshot.Revision, SafeMode: snapshot.SafeMode,
		Total: len(snapshot.Routes), Routes: make([]routeCatalogEntryView, 0, len(snapshot.Routes)),
	}
	for _, item := range snapshot.Routes {
		response.Routes = append(response.Routes, routeCatalogEntry(item))
	}
	return apphttp.OK(c, response)
}

func routeCatalogEntry(item routes.RouteCatalogRoute) routeCatalogEntryView {
	route := item.Route
	view := routeCatalogEntryView{
		RouteID: route.ID, ContractVersion: route.ContractVersion,
		Action: route.Action, TargetRouteID: route.TargetID,
		Method: route.Method, Path: route.Path, PathSignature: route.PathSignature,
		Priority: route.Priority, ProviderKind: route.Provider.Kind,
		Guard: route.Guard, Permission: route.Permission, Handler: route.Handler,
		Destination: route.Destination, Mode: route.Mode, Fallback: route.Fallback,
		RequestSchema: route.RequestSchema, ResponseSchema: route.ResponseSchema,
		TimeoutMS: route.TimeoutMS, Admitted: item.Admitted,
		ConflictKinds: make([]string, 0, len(item.ConflictKinds)),
	}
	if route.Provider.Kind == routes.ProviderPlugin {
		artifact := route.Provider.Artifact
		view.Artifact = &artifact
	}
	guardKind := ""
	if route.PluginGuard.ID != "" {
		guardKind = route.PluginGuard.Kind
	}
	view.GuardRisk = extensionmanifest.GuardRuntimeRisk(route.Guard, guardKind)
	for _, kind := range item.ConflictKinds {
		view.ConflictKinds = append(view.ConflictKinds, string(kind))
	}
	return view
}

func mapRouteCatalogError(err error) error {
	switch {
	case errors.Is(err, routes.ErrCatalogUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, routeCatalogUnavailableReason)
	case errors.Is(err, routes.ErrRevisionConflict):
		return fiber.NewError(fiber.StatusServiceUnavailable, routeCatalogUnavailableReason)
	default:
		return err
	}
}
