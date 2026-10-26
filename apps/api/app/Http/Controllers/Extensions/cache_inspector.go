package extensionscontroller

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
)

const (
	cacheInspectorDefaultLimit      = 100
	cacheInspectorMaximumLimit      = 200
	cacheInspectorInvalidReason     = "extensions.cache_inspector_invalid"
	cacheInspectorConflictReason    = "extensions.cache_inspector_conflict"
	cacheInspectorUnavailableReason = "extensions.cache_inspector_unavailable"
)

func (h *Controller) inspectCache(c fiber.Ctx) error {
	if _, err := h.routeProviderViewer(c); err != nil {
		return err
	}
	if h.cacheRegistry == nil || h.cacheInspect == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, cacheInspectorUnavailableReason)
	}
	limit, err := parseCacheInspectorLimit(c.Query("limit"))
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, cacheInspectorInvalidReason)
	}
	snapshot, err := h.cacheInspect(h.cacheRegistry, limit)
	if err != nil {
		return mapCacheInspectorError(err)
	}
	return apphttp.OK(c, snapshot)
}

func parseCacheInspectorLimit(raw string) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return cacheInspectorDefaultLimit, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > cacheInspectorMaximumLimit {
		return 0, hostapi.ErrHostCacheInspectorInvalid
	}
	return limit, nil
}

func mapCacheInspectorError(err error) error {
	switch {
	case errors.Is(err, hostapi.ErrHostCacheInspectorInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, cacheInspectorInvalidReason)
	case errors.Is(err, hostapi.ErrHostCacheInspectorConflict):
		return fiber.NewError(fiber.StatusConflict, cacheInspectorConflictReason)
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, cacheInspectorUnavailableReason)
	}
}
