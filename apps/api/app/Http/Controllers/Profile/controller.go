package profilecontroller

import (
	"errors"
	"strconv"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	attachments "github.com/keeptrue/sforum/apps/api/app/Models/Attachments"
	forum "github.com/keeptrue/sforum/apps/api/app/Models/Forum"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	profile "github.com/keeptrue/sforum/apps/api/app/Models/Profile"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service         *profile.Service
	users           identity.ActorStore
	sessions        *authsession.Manager
	forumReadPolicy ForumReadPolicySnapshot
}

type ForumReadPolicySnapshot interface {
	ForumReadPolicySnapshot() (guestRead string, softDeleteVisibility string, revision uint64, ok bool)
}

func NewController(service *profile.Service, users identity.ActorStore, sessions *authsession.Manager, forumReadPolicy ForumReadPolicySnapshot) *Controller {
	return &Controller{service: service, users: users, sessions: sessions, forumReadPolicy: forumReadPolicy}
}

// updateProfileRequest 所有字段可选（指针）；nil 表示不改。
type updateProfileRequest struct {
	Bio                *string `json:"bio"`
	Signature          *string `json:"signature"`
	Location           *string `json:"location"`
	WebsiteURL         *string `json:"websiteUrl"`
	AvatarAttachmentID *int64  `json:"avatarAttachmentId"`
	// DisplayName 是昵称。留空字符串表示「显示用户名」。
	DisplayName *string `json:"displayName"`
}

func (h *Controller) publicProfile(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	username := c.Params("username")
	data, err := h.service.GetPublicProfile(c.Context(), username)
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) publicActivities(c fiber.Ctx) error {
	if err := h.requireGuestRead(c); err != nil {
		return err
	}
	data, err := h.service.ListPublicActivities(c.Context(), profile.ListActivitiesInput{
		Username: c.Params("username"),
		Kind:     c.Query("kind"),
		Page:     profileQueryInt(c, "page"),
		PerPage:  profileQueryInt(c, "perPage"),
	})
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, data)
}

func profileQueryInt(c fiber.Ctx, key string) int {
	value, _ := strconv.Atoi(c.Query(key))
	return value
}

// 公开资料包含最近主题，必须与论坛游客阅读策略保持同一边界。
func (h *Controller) requireGuestRead(c fiber.Ctx) error {
	if h.forumReadPolicy == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	guestRead, _, revision, ok := h.forumReadPolicy.ForumReadPolicySnapshot()
	if !ok || revision == 0 {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	switch guestRead {
	case "public":
		return nil
	case "login_required":
		_, authenticated, err := apphttp.ResolveUserID(c, h.sessions)
		if err != nil {
			return err
		}
		if !authenticated {
			return fiber.NewError(fiber.StatusUnauthorized, forum.CodeGuestLoginRequired)
		}
		return nil
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
}

func (h *Controller) myProfile(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	data, err := h.service.GetMyProfile(c.Context(), actor.ID)
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, data)
}

func (h *Controller) updateMyProfile(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req updateProfileRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeProfileInvalid)
	}
	updated, err := h.service.UpdateMyProfile(c.Context(), actor, profile.UpdateProfileInput{
		Bio:                req.Bio,
		Signature:          req.Signature,
		Location:           req.Location,
		WebsiteURL:         req.WebsiteURL,
		AvatarAttachmentID: req.AvatarAttachmentID,
		DisplayName:        req.DisplayName,
	})
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) uploadAvatar(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeProfileInvalid)
	}
	file, err := fileHeader.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	updated, err := h.service.UploadAvatar(c.Context(), actor, attachments.UploadInput{
		OriginalName: fileHeader.Filename,
		ContentType:  fileHeader.Header.Get("Content-Type"),
		SizeBytes:    fileHeader.Size,
		File:         file,
	})
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) deleteAvatar(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	updated, err := h.service.DeleteAvatar(c.Context(), actor)
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, updated)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func mapProfileError(err error) error {
	var nicknameInvalid *identity.NicknameInvalidError
	var nicknameCooldown *identity.NicknameCooldownError
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	// 昵称错误走带字段/重试时刻的 APIError：错误中间件会本地化字段消息，
	// 前端能就地把原因贴在输入框旁边，冷却时还能显示「什么时候能再改」。
	case errors.Is(err, identity.ErrNicknameDisabled):
		return apphttp.NewError(fiber.StatusUnprocessableEntity, profile.CodeNicknameDisabled)
	case errors.As(err, &nicknameInvalid):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, profile.CodeNicknameInvalid, nicknameInvalid.Fields)
	case errors.As(err, &nicknameCooldown):
		return apphttp.NewErrorWithRetryAt(fiber.StatusUnprocessableEntity, profile.CodeNicknameCooldown, nicknameCooldown.RetryAt)
	case errors.Is(err, profile.ErrProfileNotFound):
		return fiber.NewError(fiber.StatusNotFound, profile.CodeProfileNotFound)
	case errors.Is(err, profile.ErrProfileInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeProfileInvalid)
	case errors.Is(err, profile.ErrAvatarUploadDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeAvatarUploadDisabled)
	case errors.Is(err, attachments.ErrUploadDisabled):
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeAvatarUploadDisabled)
	case errors.Is(err, attachments.ErrInvalidAttachment):
		return fiber.NewError(fiber.StatusUnprocessableEntity, profile.CodeProfileInvalid)
	case errors.Is(err, attachments.ErrStorageUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, attachments.CodeStorageUnavailable)
	default:
		return err
	}
}

// nicknameAvailability 是改名前的预检。它不写任何东西，只回答三件事：
// 这个值能不能用、有多少人同名、如果不能用是哪个原因。
func (h *Controller) nicknameAvailability(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	result, err := h.service.CheckNicknameAvailability(c.Context(), actor, c.Query("value"))
	if err != nil {
		return mapProfileError(err)
	}
	return apphttp.OK(c, result)
}
