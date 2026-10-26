package identitycontroller

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	"github.com/keeptrue/sforum/apps/api/app/Support/Audit"
)

type createAPITokenRequest struct {
	Name      string   `json:"name"`
	Scopes    []string `json:"scopes"`
	ExpiresAt *string  `json:"expiresAt"`
}

// loginIssuedAPIToken 是带 issueApiToken 的登录响应 data 形状。
// 明文令牌只在本次登录响应出现一次。
type loginIssuedAPIToken struct {
	User     identity.CurrentUser   `json:"user"`
	APIToken apitokens.CreatedToken `json:"apiToken"`
}

func parseAPITokenExpiry(raw *string) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, *raw)
	if err != nil {
		return nil, apitokens.ErrInvalidInput
	}
	utc := parsed.UTC()
	return &utc, nil
}

// validateLoginAPITokenRequest 只做形状校验，必须在签发会话前执行，
// 避免「会话已签发但令牌请求非法」的半成功状态。
func (h *Controller) validateLoginAPITokenRequest(input *createAPITokenRequest) error {
	if input == nil {
		return nil
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	if strings.TrimSpace(input.Name) == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "api_token.name_required")
	}
	hasScope := false
	for _, scope := range input.Scopes {
		if strings.TrimSpace(scope) != "" {
			hasScope = true
			break
		}
	}
	if !hasScope {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "api_token.scopes_required")
	}
	if _, err := parseAPITokenExpiry(input.ExpiresAt); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "api_token.invalid")
	}
	return nil
}

// issueLoginAPIToken 在密码登录成功后签发一枚 PAT。
// scopes 必须显式声明且由 Service 校验为当前账号权限的子集（与 POST /auth/tokens 同规则）。
func (h *Controller) issueLoginAPIToken(c fiber.Ctx, userID int64, input *createAPITokenRequest) (*apitokens.CreatedToken, error) {
	if input == nil {
		return nil, nil
	}
	if err := h.validateLoginAPITokenRequest(input); err != nil {
		return nil, err
	}
	expires, err := parseAPITokenExpiry(input.ExpiresAt)
	if err != nil {
		return nil, mapAPITokenError(err)
	}
	actor, err := h.service.Actor(c.Context(), userID)
	if err != nil {
		return nil, mapIdentityError(err)
	}
	created, err := h.apiTokens.Create(c.Context(), actor, apitokens.CreateInput{
		Name: input.Name, Scopes: input.Scopes, ExpiresAt: expires,
	})
	if err != nil {
		return nil, mapAPITokenError(err)
	}
	return &created, nil
}

func (h *Controller) listAPITokens(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	// PAT 管理只允许 cookie 会话，禁止用 PAT 创建/列出 PAT（降低窃取面）。
	if apitokens.TokenIDFromContext(c.Context()) > 0 {
		return fiber.NewError(fiber.StatusForbidden, "api_token.cookie_required")
	}
	includeRevoked := c.Query("includeRevoked") == "true"
	items, err := h.apiTokens.List(c.Context(), actor.ID, includeRevoked)
	if err != nil {
		return err
	}
	return apphttp.OK(c, map[string]any{"items": items})
}

func (h *Controller) createAPIToken(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	if apitokens.TokenIDFromContext(c.Context()) > 0 {
		return fiber.NewError(fiber.StatusForbidden, "api_token.cookie_required")
	}
	var req createAPITokenRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "api_token.invalid")
	}
	expires, err := parseAPITokenExpiry(req.ExpiresAt)
	if err != nil {
		return mapAPITokenError(err)
	}
	created, err := h.apiTokens.Create(c.Context(), actor, apitokens.CreateInput{
		Name: req.Name, Scopes: req.Scopes, ExpiresAt: expires,
	})
	if err != nil {
		return mapAPITokenError(err)
	}
	return apphttp.Created(c, created)
}

func (h *Controller) revokeAPIToken(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	if apitokens.TokenIDFromContext(c.Context()) > 0 {
		return fiber.NewError(fiber.StatusForbidden, "api_token.cookie_required")
	}
	id, err := strconv.ParseInt(c.Params("tokenID"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "api_token.not_found")
	}
	if err := h.apiTokens.Revoke(c.Context(), actor, id); err != nil {
		return mapAPITokenError(err)
	}
	return apphttp.NoData(c)
}

func (h *Controller) rotateAPIToken(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if h.apiTokens == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "service.not_ready")
	}
	if apitokens.TokenIDFromContext(c.Context()) > 0 {
		return fiber.NewError(fiber.StatusForbidden, "api_token.cookie_required")
	}
	id, err := strconv.ParseInt(c.Params("tokenID"), 10, 64)
	if err != nil || id <= 0 {
		return fiber.NewError(fiber.StatusNotFound, "api_token.not_found")
	}
	created, err := h.apiTokens.Rotate(c.Context(), actor, id)
	if err != nil {
		return mapAPITokenError(err)
	}
	return apphttp.OK(c, created)
}

func mapAPITokenError(err error) error {
	switch {
	case errors.Is(err, apitokens.ErrTokenNotFound):
		return fiber.NewError(fiber.StatusNotFound, "api_token.not_found")
	case errors.Is(err, apitokens.ErrInvalidInput), errors.Is(err, apitokens.ErrScopeNotAllowed):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "api_token.invalid")
	case errors.Is(err, apitokens.ErrTokenRevoked):
		return fiber.NewError(fiber.StatusConflict, "api_token.revoked")
	default:
		return err
	}
}

// auditSensitiveWithToken 在 PAT 调用敏感路由时追加审计（可选 helper）。
func (h *Controller) auditSensitiveWithToken(c fiber.Ctx, action string, meta map[string]any) {
	if h.auditor == nil {
		return
	}
	userID, ok := apitokens.UserIDFromContext(c.Context())
	if !ok {
		return
	}
	if meta == nil {
		meta = map[string]any{}
	}
	meta["tokenId"] = apitokens.TokenIDFromContext(c.Context())
	meta["via"] = "api_token"
	_ = h.auditor.Append(c.Context(), audit.Event{ActorUserID: userID, Action: action, Metadata: meta})
}
