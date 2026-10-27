package attachmentscontroller

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	uploadpolicy "github.com/keeptrue/sforum/apps/api/app/Models/Attachments/UploadPolicy"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

type Controller struct {
	service           *attachments.Service
	compression       *attachments.CompressionService
	users             identity.ActorStore
	sessions          *authsession.Manager
	emailVerification EmailVerificationGate
}

type EmailVerificationGate interface {
	RequireVerifiedForContent(context.Context, int64) error
}

const (
	mediaDeliveryHeader = "X-SForum-Media-Delivery"
	mediaDeliveryDirect = "redirect-v1"
)

func (h *Controller) WithCompressionService(service *attachments.CompressionService) *Controller {
	if h != nil {
		h.compression = service
	}
	return h
}

func NewController(service *attachments.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

func (h *Controller) WithEmailVerificationGate(gate EmailVerificationGate) *Controller {
	if h != nil {
		h.emailVerification = gate
	}
	return h
}

type updateAttachmentRequest struct {
	Status string `json:"status"`
}

type cleanupRequest struct {
	// 指针是为了区分「没传」和「传了 0」：空 body 必须走 service 的默认归一化，
	// 而不是被绑定零值顶成一次无意的批量删除。
	Limit *int `json:"limit"`
}

// decodeCleanupRequest 解析清理请求：空 body 返回 0（由 service 归一成默认值），
// 非法 JSON 显式 400。绝不把坏请求吞成零值后继续删数据。
func decodeCleanupRequest(c fiber.Ctx) (int, error) {
	if body := c.Body(); len(body) > 0 {
		var req cleanupRequest
		if err := c.Bind().Body(&req); err != nil {
			return 0, fiber.NewError(fiber.StatusBadRequest, "attachments.cleanup_body_invalid")
		}
		if req.Limit != nil {
			return *req.Limit, nil
		}
	}
	return 0, nil
}

type attachmentDTO struct {
	ID             int64                     `json:"id"`
	PublicID       string                    `json:"publicId"`
	Name           string                    `json:"name"`
	ContentType    string                    `json:"contentType"`
	Size           int64                     `json:"size"`
	ImageWidth     *int                      `json:"imageWidth,omitempty"`
	ImageHeight    *int                      `json:"imageHeight,omitempty"`
	URL            string                    `json:"url"`
	Status         string                    `json:"status"`
	Provider       string                    `json:"provider"`
	Owner          *attachments.OwnerSummary `json:"owner,omitempty"`
	ReferenceCount int                       `json:"referenceCount"`
	CreatedAt      time.Time                 `json:"createdAt"`
}

type adminAttachmentDTO struct {
	attachmentDTO
	ObjectKey  string     `json:"objectKey"`
	Extension  string     `json:"extension"`
	SHA256     string     `json:"sha256"`
	Visibility string     `json:"visibility"`
	DeletedAt  *time.Time `json:"deletedAt,omitempty"`
}

type attachmentListDTO struct {
	Items   []adminAttachmentDTO `json:"items"`
	Total   int64                `json:"total"`
	Page    int                  `json:"page"`
	PerPage int                  `json:"perPage"`
}

type attachmentDetailDTO struct {
	adminAttachmentDTO
	References []attachments.AttachmentReference `json:"references"`
}

func (h *Controller) upload(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.emailVerification != nil {
		if err := h.emailVerification.RequireVerifiedForContent(c.Context(), actor.ID); err != nil {
			return mapAttachmentError(err)
		}
	}
	policy, err := h.service.UploadPolicy(c.Context(), actor)
	if err != nil {
		return mapAttachmentError(err)
	}
	if !policy.Allowed {
		if policy.Reason == attachments.CodeUploadDisabled {
			return mapAttachmentError(attachments.ErrUploadDisabled)
		}
		return mapAttachmentError(identity.ErrPermissionDenied)
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	item, err := h.service.Upload(c.Context(), actor, attachments.UploadInput{
		OriginalName: fileHeader.Filename,
		ContentType:  fileHeader.Header.Get("Content-Type"),
		SizeBytes:    fileHeader.Size,
		File:         file,
	})
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.Created(c, toAttachmentDTO(item))
}

func (h *Controller) get(c fiber.Ctx) error {
	actor, err := h.optionalActor(c)
	if err != nil {
		return err
	}
	item, err := h.service.Get(c.Context(), actor, c.Params("publicId"))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, toAttachmentDTO(item))
}

func (h *Controller) content(c fiber.Ctx) error {
	actor, err := h.optionalActor(c)
	if err != nil {
		return err
	}
	if c.Get(mediaDeliveryHeader) != mediaDeliveryDirect {
		item, reader, openErr := h.service.OpenContent(c.Context(), actor, c.Params("publicId"))
		if openErr != nil {
			return mapAttachmentError(openErr)
		}
		return sendAttachmentContent(c, item.ContentType, item.OriginalName, reader)
	}
	delivery, err := h.service.ResolveContentDelivery(c.Context(), actor, c.Params("publicId"))
	if err != nil {
		return mapAttachmentError(err)
	}
	return sendAttachmentDelivery(c, delivery)
}

func (h *Controller) variantContent(c fiber.Ctx) error {
	actor, err := h.optionalActor(c)
	if err != nil {
		return err
	}
	if h.compression == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	if c.Get(mediaDeliveryHeader) != mediaDeliveryDirect {
		item, reader, openErr := h.compression.OpenVariant(c.Context(), actor, c.Params("publicId"), c.Params("variant"))
		if openErr != nil {
			return mapAttachmentError(openErr)
		}
		return sendAttachmentContent(c, item.ContentType, item.OriginalName, reader)
	}
	delivery, err := h.compression.ResolveVariantDelivery(c.Context(), actor, c.Params("publicId"), c.Params("variant"))
	if err != nil {
		return mapAttachmentError(err)
	}
	return sendAttachmentDelivery(c, delivery)
}

func sendAttachmentDelivery(c fiber.Ctx, delivery attachments.ContentDelivery) error {
	if delivery.RedirectURL != "" {
		// The signed location is capability-bearing and must never be cached as a
		// shared response. The object store serves the bytes after this redirect.
		c.Set(fiber.HeaderCacheControl, "private, no-store")
		return c.Redirect().Status(fiber.StatusFound).To(delivery.RedirectURL)
	}
	if delivery.Reader == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	return sendAttachmentContent(c, delivery.ContentType, delivery.OriginalName, delivery.Reader)
}

func sendAttachmentContent(c fiber.Ctx, contentType, originalName string, reader io.ReadCloser) error {
	c.Set(fiber.HeaderContentType, contentType)
	// 安全头：阻止 MIME 嗅探；主动内容类型（HTML/SVG/JS 等）强制下载而非内联渲染，
	// 避免公开附件形成同源存储型 XSS（即便入库时 denylist 被绕过，此处兜底）。
	c.Set("X-Content-Type-Options", "nosniff")
	c.Set(fiber.HeaderContentDisposition, attachmentContentDisposition(contentType, originalName))
	// SendStream 在处理器返回后读取并关闭 reader；这里提前 defer Close 会造成代理上游断流。
	return c.SendStream(reader)
}

func (h *Controller) compressionSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.compression == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	settings, err := h.compression.Settings(c.Context(), actor)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) updateCompressionSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.compression == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	var input attachments.CompressionSettings
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	}
	settings, err := h.compression.UpdateSettings(c.Context(), actor, input)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) compressionStats(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.compression == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	stats, err := h.compression.Stats(c.Context(), actor)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, stats)
}

func (h *Controller) backfillCompression(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.compression == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	}
	var input struct {
		Limit int `json:"limit"`
	}
	_ = c.Bind().Body(&input)
	result, err := h.compression.Backfill(c.Context(), actor, input.Limit)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, result)
}

// attachmentContentDisposition 按 MIME 决定内联或强制下载，并安全拼装文件名。
// 主动内容类型强制 attachment，杜绝同源 XSS；文件名中的双引号被剔除以防注入。
func attachmentContentDisposition(contentType, originalName string) string {
	disposition := "inline"
	if options.IsAttachmentActiveContentType(contentType) {
		disposition = "attachment"
	}
	// 剥离 CR/LF 与双引号，防止 Content-Disposition 头注入。
	safeName := strings.NewReplacer("\r", "", "\n", "", `"`, "").Replace(originalName)
	return disposition + `; filename="` + safeName + `"`
}

func (h *Controller) settings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	settings, err := h.service.Settings(c.Context(), actor, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) updateSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req attachments.AttachmentSettings
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	}
	settings, err := h.service.UpdateSettings(c.Context(), actor, req, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, settings)
}

func (h *Controller) testSettings(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	result, err := h.service.Probe(c.Context(), actor, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) listStorageInstances(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListStorageInstances(c.Context(), actor, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) createStorageInstance(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input attachments.StorageInstanceInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeStorageInstanceInvalid)
	}
	item, err := h.service.CreateStorageInstance(c.Context(), actor, input, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) updateStorageInstance(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input attachments.StorageInstanceInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeStorageInstanceInvalid)
	}
	item, err := h.service.UpdateStorageInstance(c.Context(), actor, c.Params("id"), input, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) probeStorageInstance(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var input attachments.StorageInstanceProbeInput
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeStorageInstanceInvalid)
	}
	result, err := h.service.ProbeStorageInstance(c.Context(), actor, input, apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) activateStorageInstance(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.ActivateStorageInstance(c.Context(), actor, c.Params("id"), apphttp.Locale(c))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) activateLocalStorage(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.service.RestoreLocalStorage(c.Context(), actor); err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, map[string]any{"provider": "local", "active": true})
}

func (h *Controller) deleteStorageInstance(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if err := h.service.DeleteStorageInstance(c.Context(), actor, c.Params("id")); err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, fiber.Map{"deleted": true})
}

func (h *Controller) listAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	list, err := h.service.List(c.Context(), actor, attachments.AttachmentListInput{
		Page:            queryInt(c, "page"),
		PerPage:         queryInt(c, "perPage"),
		Query:           c.Query("query"),
		Provider:        c.Query("provider"),
		Status:          c.Query("status"),
		ContentType:     c.Query("contentType"),
		OwnerUserID:     int64(queryInt(c, "ownerUserId")),
		ReferenceStatus: c.Query("referenceStatus"),
		CreatedFrom:     queryTime(c, "createdFrom"),
		CreatedTo:       queryTime(c, "createdTo"),
	})
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, toAttachmentListDTO(list))
}

func (h *Controller) detailAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	detail, err := h.service.Detail(c.Context(), actor, int64(queryParamInt(c, "id")))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, toAttachmentDetailDTO(detail))
}

func (h *Controller) updateAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateAttachmentRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	}
	item, err := h.service.UpdateStatus(c.Context(), actor, int64(queryParamInt(c, "id")), req.Status)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, toAdminAttachmentDTO(item))
}

func (h *Controller) deleteAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	item, err := h.service.Delete(c.Context(), actor, int64(queryParamInt(c, "id")))
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, toAdminAttachmentDTO(item))
}

func (h *Controller) cleanupAdmin(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	limit, err := decodeCleanupRequest(c)
	if err != nil {
		return err
	}
	result, err := h.service.Cleanup(c.Context(), actor, limit)
	if err != nil {
		return mapAttachmentError(err)
	}
	return apphttp.OK(c, result)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func (h *Controller) optionalActor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.OptionalActor(c, h.sessions, h.users)
}

func mapAttachmentError(err error) error {
	var rejected *appevents.RejectedError
	var tooLarge *attachments.FileTooLargeError
	switch {
	case errors.Is(err, identity.ErrEmailVerificationRequired):
		return fiber.NewError(fiber.StatusForbidden, "auth.email_verification_required")
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, attachments.ErrGuestLoginRequired):
		return fiber.NewError(fiber.StatusUnauthorized, attachments.CodeGuestLoginRequired)
	// 插件 attachment.before_upload 等同步拒绝：422 + 稳定 reason。
	case errors.As(err, &rejected):
		return fiber.NewError(fiber.StatusUnprocessableEntity, rejected.Reason)
	case errors.As(err, &tooLarge):
		return fiber.NewError(fiber.StatusRequestEntityTooLarge, attachments.CodeFileTooLarge)
	case errors.Is(err, uploadpolicy.ErrProtectedActor):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadPolicyProtected)
	case errors.Is(err, uploadpolicy.ErrInvalidPolicy):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadPolicyInvalid)
	case errors.Is(err, attachments.ErrUploadDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeUploadDisabled)
	case errors.Is(err, attachments.ErrInvalidAttachment):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeInvalidAttachment)
	case errors.Is(err, attachments.ErrReferenced):
		return fiber.NewError(fiber.StatusConflict, attachments.CodeReferenced)
	case errors.Is(err, attachments.ErrStorageInstanceReferenced):
		return fiber.NewError(fiber.StatusConflict, attachments.CodeStorageInstanceReferenced)
	case errors.Is(err, attachments.ErrStorageInstanceInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, attachments.CodeStorageInstanceInvalid)
	case errors.Is(err, attachments.ErrAttachmentNotFound):
		return fiber.NewError(fiber.StatusNotFound, attachments.CodeInvalidAttachment)
	case errors.Is(err, attachments.ErrStorageUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	default:
		return err
	}
}

func toAttachmentDTO(item attachments.Attachment) attachmentDTO {
	return attachmentDTO{
		ID:             item.ID,
		PublicID:       item.PublicID,
		Name:           item.OriginalName,
		ContentType:    item.ContentType,
		Size:           item.SizeBytes,
		ImageWidth:     item.ImageWidth,
		ImageHeight:    item.ImageHeight,
		URL:            item.URL,
		Status:         item.Status,
		Provider:       item.Provider,
		Owner:          item.Owner,
		ReferenceCount: item.ReferenceCount,
		CreatedAt:      item.CreatedAt,
	}
}

func toAdminAttachmentDTO(item attachments.Attachment) adminAttachmentDTO {
	return adminAttachmentDTO{
		attachmentDTO: toAttachmentDTO(item),
		ObjectKey:     item.ObjectKey,
		Extension:     item.Extension,
		SHA256:        item.SHA256,
		Visibility:    item.Visibility,
		DeletedAt:     item.DeletedAt,
	}
}

func toAttachmentListDTO(list attachments.AttachmentList) attachmentListDTO {
	items := make([]adminAttachmentDTO, 0, len(list.Items))
	for _, item := range list.Items {
		items = append(items, toAdminAttachmentDTO(item))
	}
	return attachmentListDTO{Items: items, Total: list.Total, Page: list.Page, PerPage: list.PerPage}
}

func toAttachmentDetailDTO(detail attachments.AttachmentDetail) attachmentDetailDTO {
	return attachmentDetailDTO{
		adminAttachmentDTO: toAdminAttachmentDTO(detail.Attachment),
		References:         detail.References,
	}
}

func queryInt(c fiber.Ctx, key string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(c.Query(key)))
	return value
}

func queryParamInt(c fiber.Ctx, key string) int {
	value, _ := strconv.Atoi(strings.TrimSpace(c.Params(key)))
	return value
}

func queryTime(c fiber.Ctx, key string) time.Time {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return time.Time{}
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed
	}
	return time.Time{}
}
