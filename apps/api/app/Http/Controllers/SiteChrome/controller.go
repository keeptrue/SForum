package sitechromecontroller

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	sitechrome "github.com/keeptrue/sforum/apps/api/app/Models/SiteChrome"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
)

type Controller struct {
	service     *sitechrome.Service
	brandAssets *sitechrome.BrandAssetService
	users       identity.ActorStore
	sessions    *authsession.Manager
}

func NewController(service *sitechrome.Service, users identity.ActorStore, sessions *authsession.Manager) *Controller {
	return &Controller{service: service, users: users, sessions: sessions}
}

type navItemRequest struct {
	LabelZhCN    string `json:"labelZhCN"`
	LabelEnUS    string `json:"labelEnUS"`
	Href         string `json:"href"`
	OpenInNewTab bool   `json:"openInNewTab"`
	Position     int    `json:"position"`
	Enabled      bool   `json:"enabled"`
}

type updateNavItemRequest struct {
	LabelZhCN    *string `json:"labelZhCN"`
	LabelEnUS    *string `json:"labelEnUS"`
	Href         *string `json:"href"`
	OpenInNewTab *bool   `json:"openInNewTab"`
	Position     *int    `json:"position"`
	Enabled      *bool   `json:"enabled"`
}

type friendLinkRequest struct {
	Name        string `json:"name"`
	URL         string `json:"url"`
	Description string `json:"description"`
	LogoURL     string `json:"logoUrl"`
	Position    int    `json:"position"`
	Enabled     bool   `json:"enabled"`
}

type updateFriendLinkRequest struct {
	Name        *string `json:"name"`
	URL         *string `json:"url"`
	Description *string `json:"description"`
	LogoURL     *string `json:"logoUrl"`
	Position    *int    `json:"position"`
	Enabled     *bool   `json:"enabled"`
}

type announcementRequest struct {
	TitleZhCN   string  `json:"titleZhCN"`
	TitleEnUS   string  `json:"titleEnUS"`
	BodyZhCN    string  `json:"bodyZhCN"`
	BodyEnUS    string  `json:"bodyEnUS"`
	Style       string  `json:"style"`
	Href        string  `json:"href"`
	Dismissible bool    `json:"dismissible"`
	Position    int     `json:"position"`
	Enabled     bool    `json:"enabled"`
	StartsAt    *string `json:"startsAt"`
	EndsAt      *string `json:"endsAt"`
}

// publicNavResponse 公开顶栏（E2.3）：items 为运营配置；extensionItems 为插件贡献。
// 合并顺序由主题负责：核心/运营 items 在前，extensionItems 按 order 次之。
type publicNavResponse struct {
	Items          []sitechrome.NavItem          `json:"items"`
	ExtensionItems []sitechrome.ExtensionNavItem `json:"extensionItems,omitempty"`
}

func (h *Controller) publicNavItems(c fiber.Ctx) error {
	items, err := h.service.ListPublicNavItems(c.Context())
	if err != nil {
		return mapError(err)
	}
	if items == nil {
		items = []sitechrome.NavItem{}
	}
	return apphttp.OK(c, publicNavResponse{
		Items:          items,
		ExtensionItems: h.service.ListPublicExtensionNavItems(c.Context()),
	})
}

func (h *Controller) publicNavigation(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Vary(fiber.HeaderCookie, fiber.HeaderAuthorization, fiber.HeaderAcceptLanguage)
	actor, err := apphttp.OptionalActor(c, h.sessions, h.users)
	if err != nil {
		return err
	}
	var locations []string
	if raw := strings.TrimSpace(c.Query("locations")); raw != "" {
		locations = strings.Split(raw, ",")
	}
	resolved, err := h.service.ResolvePublicNavigation(c.Context(), actor, c.Get("Accept-Language"), locations)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, resolved)
}

func (h *Controller) accountSettingsNavigation(c fiber.Ctx) error {
	c.Set(fiber.HeaderCacheControl, "private, no-store")
	c.Vary(fiber.HeaderCookie, fiber.HeaderAuthorization, fiber.HeaderAcceptLanguage)
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ResolveAccountSettingsNavigation(c.Context(), actor, c.Get("Accept-Language"))
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, fiber.Map{"items": items})
}

func (h *Controller) adminNavItems(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListAdminNavItems(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateNavItem(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req navItemRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	item, err := h.service.CreateNavItem(c.Context(), actor, sitechrome.CreateNavItemInput{
		LabelZhCN: req.LabelZhCN, LabelEnUS: req.LabelEnUS, Href: req.Href,
		OpenInNewTab: req.OpenInNewTab, Position: req.Position, Enabled: req.Enabled,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateNavItem(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("itemID"))
	if err != nil {
		return err
	}
	var req updateNavItemRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	item, err := h.service.UpdateNavItem(c.Context(), actor, sitechrome.UpdateNavItemInput{
		ID: id, LabelZhCN: req.LabelZhCN, LabelEnUS: req.LabelEnUS, Href: req.Href,
		OpenInNewTab: req.OpenInNewTab, Position: req.Position, Enabled: req.Enabled,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminDeleteNavItem(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("itemID"))
	if err != nil {
		return err
	}
	if err := h.service.DeleteNavItem(c.Context(), actor, id); err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, fiber.Map{"deleted": true})
}

func (h *Controller) publicFriendLinks(c fiber.Ctx) error {
	items, err := h.service.ListPublicFriendLinks(c.Context())
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminFriendLinks(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListAdminFriendLinks(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateFriendLink(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req friendLinkRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	item, err := h.service.CreateFriendLink(c.Context(), actor, sitechrome.CreateFriendLinkInput{
		Name: req.Name, URL: req.URL, Description: req.Description, LogoURL: req.LogoURL,
		Position: req.Position, Enabled: req.Enabled,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateFriendLink(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("linkID"))
	if err != nil {
		return err
	}
	var req updateFriendLinkRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	item, err := h.service.UpdateFriendLink(c.Context(), actor, sitechrome.UpdateFriendLinkInput{
		ID: id, Name: req.Name, URL: req.URL, Description: req.Description, LogoURL: req.LogoURL,
		Position: req.Position, Enabled: req.Enabled,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminDeleteFriendLink(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("linkID"))
	if err != nil {
		return err
	}
	if err := h.service.DeleteFriendLink(c.Context(), actor, id); err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, fiber.Map{"deleted": true})
}

func (h *Controller) publicAnnouncements(c fiber.Ctx) error {
	items, err := h.service.ListPublicAnnouncements(c.Context())
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminAnnouncements(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	items, err := h.service.ListAdminAnnouncements(c.Context(), actor)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, items)
}

func (h *Controller) adminCreateAnnouncement(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	var req announcementRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	startsAt, err := parseOptionalTime(req.StartsAt)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	endsAt, err := parseOptionalTime(req.EndsAt)
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	item, err := h.service.CreateAnnouncement(c.Context(), actor, sitechrome.CreateAnnouncementInput{
		TitleZhCN: req.TitleZhCN, TitleEnUS: req.TitleEnUS, BodyZhCN: req.BodyZhCN, BodyEnUS: req.BodyEnUS,
		Style: req.Style, Href: req.Href, Dismissible: req.Dismissible, Position: req.Position, Enabled: req.Enabled,
		StartsAt: startsAt, EndsAt: endsAt,
	})
	if err != nil {
		return mapError(err)
	}
	return apphttp.Created(c, item)
}

func (h *Controller) adminUpdateAnnouncement(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("announcementID"))
	if err != nil {
		return err
	}
	// 用 map 解析，兼容 startsAt/endsAt 为 null 时清空时间窗。
	var raw map[string]any
	if err := c.Bind().Body(&raw); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	input := sitechrome.UpdateAnnouncementInput{ID: id}
	if v, ok := raw["titleZhCN"].(string); ok {
		input.TitleZhCN = &v
	}
	if v, ok := raw["titleEnUS"].(string); ok {
		input.TitleEnUS = &v
	}
	if v, ok := raw["bodyZhCN"].(string); ok {
		input.BodyZhCN = &v
	}
	if v, ok := raw["bodyEnUS"].(string); ok {
		input.BodyEnUS = &v
	}
	if v, ok := raw["style"].(string); ok {
		input.Style = &v
	}
	if v, ok := raw["href"].(string); ok {
		input.Href = &v
	}
	if v, ok := raw["dismissible"].(bool); ok {
		input.Dismissible = &v
	}
	if v, ok := raw["position"].(float64); ok {
		n := int(v)
		input.Position = &n
	}
	if v, ok := raw["enabled"].(bool); ok {
		input.Enabled = &v
	}
	if _, present := raw["startsAt"]; present {
		if raw["startsAt"] == nil {
			input.ClearStartsAt = true
		} else if s, ok := raw["startsAt"].(string); ok {
			ts, err := parseOptionalTime(&s)
			if err != nil {
				return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
			}
			input.StartsAt = ts
		}
	}
	if _, present := raw["endsAt"]; present {
		if raw["endsAt"] == nil {
			input.ClearEndsAt = true
		} else if s, ok := raw["endsAt"].(string); ok {
			ts, err := parseOptionalTime(&s)
			if err != nil {
				return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
			}
			input.EndsAt = ts
		}
	}

	item, err := h.service.UpdateAnnouncement(c.Context(), actor, input)
	if err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, item)
}

func (h *Controller) adminDeleteAnnouncement(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	id, err := parseID(c.Params("announcementID"))
	if err != nil {
		return err
	}
	if err := h.service.DeleteAnnouncement(c.Context(), actor, id); err != nil {
		return mapError(err)
	}
	return apphttp.OK(c, fiber.Map{"deleted": true})
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	return apphttp.LoadActor(c, h.sessions, h.users)
}

func parseID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	}
	return id, nil
}

func parseOptionalTime(raw *string) (*time.Time, error) {
	if raw == nil {
		return nil, nil
	}
	value := *raw
	if value == "" {
		return nil, nil
	}
	// 接受 RFC3339（含 Z）与常见 date-time。
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, err
	}
	utc := parsed.UTC()
	return &utc, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, sitechrome.ErrInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, sitechrome.CodeInvalid)
	case errors.Is(err, sitechrome.ErrNotFound):
		return fiber.NewError(fiber.StatusNotFound, sitechrome.CodeNotFound)
	case errors.Is(err, sitechrome.ErrConflict):
		return fiber.NewError(fiber.StatusConflict, sitechrome.CodeConflict)
	default:
		return err
	}
}
