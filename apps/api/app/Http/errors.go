package http

import (
	"errors"
	"log/slog"

	"github.com/gofiber/fiber/v3"
)

func errorHandler(logger *slog.Logger) fiber.ErrorHandler {
	// 依赖故障的告警在进程内限频；同一个 reporter 服务这次装配出的整个 app。
	storage := newStorageFailureReporter(logger)
	return func(c fiber.Ctx, err error) error {
		// limiter 把 storage 故障原样上抛。它既不是 500（服务端代码没错），更不是
		// 403（不是权限问题）：依赖不可用 → 503 + Retry-After。
		if cause, ok := storageFailureCause(err); ok {
			storage.warn(c, cause)
			return storageUnavailable(c)
		}

		status := fiber.StatusInternalServerError
		reason := "internal_error"
		var apiErr *APIError

		if errors.As(err, &apiErr) {
			if apiErr.RetryAt != nil {
				return ErrorResponseWithRetryAt(c, apiErr.Status, apiErr.Reason, *apiErr.RetryAt)
			}
			fields := LocalizeFields(c, apiErr.Fields)
			if apiErr.Details != nil {
				return ErrorResponseWithFieldsAndDetails(c, apiErr.Status, apiErr.Reason, fields, apiErr.Details)
			}
			return ErrorResponseWithFields(c, apiErr.Status, apiErr.Reason, fields)
		}

		if fiberErr, ok := err.(*fiber.Error); ok {
			status = fiberErr.Code
			reason = normalizeFiberErrorReason(status, fiberErr.Message)
		} else {
			logger.Error("request failed", "error", err)
		}

		return ErrorResponse(c, status, reason)
	}
}

func normalizeFiberErrorReason(status int, message string) string {
	switch {
	case message == "":
		return "internal_error"
	case status == fiber.StatusNotFound && message == "Not Found":
		return "not_found"
	case status == fiber.StatusMethodNotAllowed && message == "Method Not Allowed":
		return "method_not_allowed"
	default:
		return message
	}
}
