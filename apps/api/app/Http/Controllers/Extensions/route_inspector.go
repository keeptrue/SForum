package extensionscontroller

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	routes "github.com/keeptrue/sforum/apps/api/app/Support/Routes"
)

const (
	routeInspectorInvalidReason     = "extensions.route_inspector_invalid"
	routeInspectorUnavailableReason = "extensions.route_inspector_unavailable"
)

func (h *Controller) inspectRoute(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeInspector == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeInspectorUnavailableReason)
	}
	method := strings.TrimSpace(c.Query("method"))
	requestPath := strings.TrimSpace(c.Query("path"))
	if method == "" || requestPath == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeInspectorInvalidReason)
	}
	snapshot, err := h.routeInspector.Inspect(c.Context(), method, requestPath)
	if err != nil {
		return mapRouteInspectorError(err)
	}
	return apphttp.OK(c, snapshot)
}

// inspectRouteIncidents 返回崩溃循环隔离的持久状态（M5：重启后管理端与 API 必须
// 报出同一份状态）。读取有界且已脱敏；空列表就是正常状态，不是错误。
func (h *Controller) inspectRouteIncidents(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.routeIncidents == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeInspectorUnavailableReason)
	}
	records, err := h.routeIncidents.ListRecentRouteRuntimeIncidents(
		c.Context(), apphttp.RouteRuntimeIncidentReadMaximum,
	)
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, routeInspectorUnavailableReason)
	}
	return apphttp.OK(c, map[string]any{
		"limit":     apphttp.RouteRuntimeIncidentReadMaximum,
		"summary":   apphttp.SummarizeRouteRuntimeIncidents(records),
		"incidents": apphttp.RouteRuntimeIncidentViews(records),
	})
}

func mapRouteInspectorError(err error) error {
	switch {
	case errors.Is(err, routes.ErrInspectorInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeInspectorInvalidReason)
	case errors.Is(err, routes.ErrRevisionConflict),
		errors.Is(err, routes.ErrProviderSelectionRevisionConflict):
		return fiber.NewError(fiber.StatusConflict, routeProviderConflictReason)
	case errors.Is(err, routes.ErrProviderSelectionInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, routeInspectorInvalidReason)
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, routeInspectorUnavailableReason)
	}
}
