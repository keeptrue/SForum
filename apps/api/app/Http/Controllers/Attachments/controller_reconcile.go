package attachmentscontroller

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
)

// reconcileRequest 使用 *bool：nil（字段缺省）视为 dry-run，
// 绝不让漏传参数退化成「直接改数据」。
type reconcileRequest struct {
	DryRun *bool `json:"dryRun"`
}

// decodeReconcileRequest 解析请求体并返回是否 dry-run。
// 必须先判空 body：fiber 的 Bind().Body() 按 Content-Type 分派，空 body 且无
// Content-Type 时直接返回 422，而契约声明 requestBody 是可选的。空 body 一律
// 视为 dry-run（fail-safe），非空但非法/类型不符仍然 400，不放宽。
func decodeReconcileRequest(c fiber.Ctx) (bool, error) {
	if body := c.Body(); len(body) > 0 {
		var req reconcileRequest
		if err := c.Bind().Body(&req); err != nil {
			return false, fiber.NewError(fiber.StatusBadRequest, "attachments.reconcile_body_invalid")
		}
		if req.DryRun != nil {
			return *req.DryRun, nil
		}
	}
	return true, nil
}

func (h *Controller) reconcileAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	dryRun, err := decodeReconcileRequest(c)
	if err != nil {
		return err
	}
	// service 层还有 PermissionAttachmentManage 第二道检查。
	result, err := h.service.ReconcileReferenceCounts(c.Context(), actor, dryRun)
	if err != nil {
		switch {
		case errors.Is(err, attachments.ErrReferenceReconcileUnsupported):
			return fiber.NewError(fiber.StatusUnprocessableEntity, "attachments.reconcile_unsupported")
		default:
			return mapAttachmentError(err)
		}
	}
	return apphttp.OK(c, result)
}
