package forumcontroller

// 本文件承载 Forum HTTP 层的 error→HTTP 映射。它会在每新增一个具名 reason 时被
// 触碰，与请求/响应编解码职责分离，避免 controller.go 重新膨胀过千行基线。

import (
	"errors"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
)

func mapForumError(err error) error {
	var rejected *appevents.RejectedError
	var cooldown *forum.CooldownError
	switch {
	case errors.Is(err, identity.ErrEmailVerificationRequired):
		return fiber.NewError(fiber.StatusForbidden, "auth.email_verification_required")
	case errors.As(err, &rejected):
		return fiber.NewError(fiber.StatusUnprocessableEntity, rejected.Reason)
	case errors.As(err, &cooldown) && errors.Is(cooldown, forum.ErrTopicCooldown):
		return apphttp.NewErrorWithRetryAt(fiber.StatusTooManyRequests, forum.CodeTopicCooldown, cooldown.RetryAt)
	case errors.As(err, &cooldown) && errors.Is(cooldown, forum.ErrCommentCooldown):
		return apphttp.NewErrorWithRetryAt(fiber.StatusTooManyRequests, forum.CodeCommentCooldown, cooldown.RetryAt)
	case errors.Is(err, ErrSearchUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "forum.search_unavailable")
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, forum.ErrAttachmentUnavailable):
		// 附件失效是「用户能自己修」的问题：给具名 reason + 正文级字段，
		// 前端据此提示重新上传，而不是只显示「服务器暂时不可用」。
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, forum.CodeAttachmentUnavailable, map[string][]string{
			"content": {forum.CodeAttachmentUnavailable},
		})
	case errors.Is(err, forum.ErrAttachmentConflict):
		return fiber.NewError(fiber.StatusConflict, forum.CodeAttachmentConflict)
	case errors.Is(err, forum.ErrInvalidContent):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidContent)
	case errors.Is(err, forum.ErrInvalidTopic):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTopic)
	case errors.Is(err, forum.ErrTopicNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeTopicNotFound)
	case errors.Is(err, forum.ErrCommentNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeCommentNotFound)
	case errors.Is(err, forum.ErrTopicClosed):
		return fiber.NewError(fiber.StatusConflict, forum.CodeTopicClosed)
	case errors.Is(err, forum.ErrInvalidTag):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidTag)
	case errors.Is(err, forum.ErrTagNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeTagNotFound)
	case errors.Is(err, forum.ErrCategoryNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeCategoryNotFound)
	case errors.Is(err, forum.ErrCategoryGroupNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeCategoryGroupNotFound)
	case errors.Is(err, forum.ErrCategoryNotEmpty):
		return fiber.NewError(fiber.StatusConflict, forum.CodeCategoryNotEmpty)
	case errors.Is(err, forum.ErrCategoryGroupNotEmpty):
		return fiber.NewError(fiber.StatusConflict, forum.CodeCategoryGroupNotEmpty)
	case errors.Is(err, forum.ErrCategoryIsDefault):
		return fiber.NewError(fiber.StatusConflict, forum.CodeCategoryIsDefault)
	case errors.Is(err, forum.ErrInvalidSettings):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidSettings)
	case errors.Is(err, forum.ErrInvalidAction):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeInvalidAction)
	case errors.Is(err, forum.ErrInvalidReferenceSelector):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeReferenceSelectorInvalid)
	case errors.Is(err, forum.ErrTitleTooShort):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeTitleTooShort)
	case errors.Is(err, forum.ErrTitleTooLong):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeTitleTooLong)
	case errors.Is(err, forum.ErrContentTooShort):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeContentTooShort)
	case errors.Is(err, forum.ErrContentTooLong):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeContentTooLong)
	case errors.Is(err, forum.ErrCommentTooShort):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeCommentTooShort)
	case errors.Is(err, forum.ErrCommentTooLong):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeCommentTooLong)
	case errors.Is(err, forum.ErrCommentNestingDeep):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeCommentNestingDeep)
	case errors.Is(err, forum.ErrEditWindowExpired):
		return fiber.NewError(fiber.StatusConflict, forum.CodeEditWindowExpired)
	case errors.Is(err, forum.ErrTopicCooldown):
		return fiber.NewError(fiber.StatusTooManyRequests, forum.CodeTopicCooldown)
	case errors.Is(err, forum.ErrCommentCooldown):
		return fiber.NewError(fiber.StatusTooManyRequests, forum.CodeCommentCooldown)
	case errors.Is(err, forum.ErrDailyTopicLimit):
		return fiber.NewError(fiber.StatusTooManyRequests, forum.CodeDailyTopicLimit)
	case errors.Is(err, forum.ErrDailyCommentLimit):
		return fiber.NewError(fiber.StatusTooManyRequests, forum.CodeDailyCommentLimit)
	case errors.Is(err, forum.ErrTagMinRequired):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeTagMinRequired)
	case errors.Is(err, forum.ErrOutboundLinkForbidden):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeOutboundLinkForbidden)
	case errors.Is(err, forum.ErrMentionsLimit):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeMentionsLimit)
	case errors.Is(err, forum.ErrDuplicateTitle):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeDuplicateTitle)
	case errors.Is(err, forum.ErrGuestLoginRequired):
		return fiber.NewError(fiber.StatusUnauthorized, forum.CodeGuestLoginRequired)
	case errors.Is(err, forum.ErrRevisionNotFound):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeRevisionNotFound)
	case errors.Is(err, forum.ErrRevisionRedacted):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionRedacted)
	case errors.Is(err, forum.ErrRevisionConflict):
		return fiber.NewError(fiber.StatusConflict, forum.CodeRevisionConflict)
	case errors.Is(err, forum.ErrRevisionReasonRequired):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionReasonRequired)
	case errors.Is(err, forum.ErrRevisionNotRestorable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionNotRestorable)
	case errors.Is(err, forum.ErrRevisionAttachmentUnavailable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionAttachmentUnavailable)
	case errors.Is(err, forum.ErrRevisionCategoryUnavailable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionCategoryUnavailable)
	case errors.Is(err, forum.ErrRevisionTagUnavailable):
		return fiber.NewError(fiber.StatusUnprocessableEntity, forum.CodeRevisionTagUnavailable)
	case errors.Is(err, forum.ErrRevisionRedactionForbidden):
		return fiber.NewError(fiber.StatusForbidden, forum.CodeRevisionRedactionForbidden)
	case errors.Is(err, forum.ErrUseSearchEndpoint):
		return fiber.NewError(fiber.StatusBadRequest, forum.CodeUseSearch)
	case errors.Is(err, forum.ErrInvalidCursor):
		return fiber.NewError(fiber.StatusBadRequest, forum.CodeInvalidCursor)
	case errors.Is(err, forum.ErrCommentRevisionUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, forum.CodeCommentStreamUnavailable)
	default:
		return err
	}
}

// reindex / search 相关 sentinel error。controller 不依赖 search 包，
// 由 bootstrap adapter 将 search 错误转换为这些 controller 级别错误。
// 导出以便 bootstrap adapter 通过 errors.Is 引用。
var (
	ErrReindexRunning    = errors.New("forum: reindex already running")
	ErrReindexNoRun      = errors.New("forum: no reindex run")
	ErrSearchUnavailable = errors.New("forum: search engine unavailable")
)

// mapReindexError 将 reindex 错误映射为 HTTP 响应。
func mapReindexError(err error) error {
	switch {
	case errors.Is(err, ErrReindexRunning):
		return fiber.NewError(fiber.StatusConflict, forum.CodeReindexRunning)
	case errors.Is(err, ErrReindexNoRun):
		return fiber.NewError(fiber.StatusNotFound, forum.CodeReindexNoRun)
	default:
		return err
	}
}
