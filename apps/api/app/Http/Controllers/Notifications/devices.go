package notificationscontroller

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

// 原生推送设备注册：自服务，所有权由 store 层 user_id 过滤保证（与登录设备一致）。

func (h *Controller) listPushDevices(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.pushDevices == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.push_devices_unavailable")
	}
	items, err := h.pushDevices.ListPushDevices(c.Context(), userID, c.Query("includeRevoked") == "true")
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) registerPushDevice(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	if h.pushDevices == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "notification.push_devices_unavailable")
	}
	var body struct {
		DeviceID   string `json:"deviceId"`
		Platform   string `json:"platform"`
		Token      string `json:"token"`
		AppVersion string `json:"appVersion"`
		Locale     string `json:"locale"`
		DeviceName string `json:"deviceName"`
	}
	if err := c.Bind().Body(&body); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.push_device_invalid")
	}
	// 控制器先做领域校验，保证任何 store 实现都不会收到未校验的输入。
	input, err := notifications.NormalizePushDeviceInput(notifications.RegisterPushDeviceInput{
		UserID:     userID,
		DeviceID:   body.DeviceID,
		Platform:   body.Platform,
		Token:      body.Token,
		AppVersion: body.AppVersion,
		Locale:     body.Locale,
		DeviceName: body.DeviceName,
	})
	if err != nil {
		return pushDeviceValidationError(err)
	}
	item, err := h.pushDevices.RegisterPushDevice(c.Context(), input)
	if err != nil {
		if mapped := pushDeviceValidationError(err); mapped != nil {
			return mapped
		}
		return err
	}
	h.appendAudit(c, userID, audit.ActionPushDeviceRegister, map[string]any{
		"deviceId": item.DeviceID, "platform": item.Platform,
	})
	return apphttp.OK(c, item)
}

func (h *Controller) revokePushDevice(c fiber.Ctx) error {
	userID, err := h.userID(c)
	if err != nil {
		return err
	}
	deviceID := strings.TrimSpace(c.Params("deviceId"))
	if h.pushDevices == nil || deviceID == "" {
		return fiber.NewError(fiber.StatusNotFound, "notification.push_device_not_found")
	}
	if err := h.pushDevices.RevokePushDevice(c.Context(), userID, deviceID); err != nil {
		if errors.Is(err, notifications.ErrPushDeviceInvalid) {
			return fiber.NewError(fiber.StatusNotFound, "notification.push_device_not_found")
		}
		// 不存在或不属于当前用户统一按未找到处理，避免暴露设备归属。
		return fiber.NewError(fiber.StatusNotFound, "notification.push_device_not_found")
	}
	h.appendAudit(c, userID, audit.ActionPushDeviceRevoke, map[string]any{"deviceId": deviceID})
	return apphttp.OK(c, map[string]any{"revoked": true})
}

// pushDeviceValidationError 把领域校验错误映射为稳定 reason；非校验错误返回 nil。
func pushDeviceValidationError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, notifications.ErrPushDevicePlatformKnown):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.push_device_platform_unsupported")
	case errors.Is(err, notifications.ErrPushDeviceTokenTooLong):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.push_device_token_too_long")
	case errors.Is(err, notifications.ErrPushDeviceInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "notification.push_device_invalid")
	default:
		return nil
	}
}
