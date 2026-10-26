package identitycontroller

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	useragent "github.com/keeptrue/sforum/apps/api/app/Support/UserAgent"
)

func (h *Controller) sessionUserID(c fiber.Ctx) (int64, bool, error) {
	// Bearer PAT 优先于 cookie session（机器调用场景）。
	if userID, ok := apitokens.UserIDFromContext(c.Context()); ok {
		return userID, true, nil
	}
	return h.authSessions.CurrentUserID(c)
}

func (h *Controller) sessionUserIDWithoutRenewal(c fiber.Ctx) (int64, bool, error) {
	if userID, ok := apitokens.UserIDFromContext(c.Context()); ok {
		return userID, true, nil
	}
	return h.authSessions.CurrentUserIDWithoutRenewal(c)
}

// runRiskEvaluation 组合活跃 risk 提供方；无接线时保持 Core 默认放行。
func (h *Controller) runRiskEvaluation(ctx context.Context, userID int64, purpose string) error {
	if h == nil || h.riskPolicy == nil {
		return nil
	}
	_, err := h.riskPolicy.RequireAllow(ctx, identity.RiskEvaluationInput{
		UserID: userID, Purpose: purpose,
	})
	return err
}

// runSessionIssue keeps the Host issue mutation inside exact policy admission.
// Nil evaluator preserves the Core default and executes the effect directly.
func (h *Controller) runSessionIssue(
	ctx context.Context,
	userID int64,
	tokenVersion int64,
	stepUpEvidence string,
	effect identity.SessionPolicyHostEffect,
) error {
	if effect == nil {
		return identity.ErrSessionPolicyEvaluationInvalid
	}
	if h == nil || h.sessionPolicy == nil {
		return effect(ctx)
	}
	result, err := h.sessionPolicy.RequireAllowAndRun(
		ctx,
		identity.SessionEvaluationInput{
			UserID: userID, TokenVersion: tokenVersion, Purpose: identity.SessionEvaluationPurposeIssue,
			StepUpEvidenceToken: stepUpEvidence,
		},
		effect,
	)
	if errors.Is(err, identity.ErrSessionPolicyEvaluationStepUp) && result.StepUpToken != "" {
		return &identity.SessionPolicyStepUpRequiredError{
			Token: result.StepUpToken, ExpiresAt: result.StepUpExpiresAt,
		}
	}
	return err
}

func (h *Controller) beginSessionIssue(
	c fiber.Ctx,
	ctx context.Context,
	userID int64,
	tokenVersion int64,
) (*authsession.Pending, error) {
	if h.sessionPolicy == nil {
		return h.authSessions.BeginWithContext(c, ctx, userID)
	}
	return h.authSessions.BeginWithAuthorityVersion(c, ctx, userID, tokenVersion)
}

func (h *Controller) actor(c fiber.Ctx) (identity.Actor, error) {
	userID, ok, err := h.sessionUserID(c)
	return h.actorForUserID(c, userID, ok, err)
}

func (h *Controller) actorWithoutSessionRenewal(c fiber.Ctx) (identity.Actor, error) {
	userID, ok, err := h.sessionUserIDWithoutRenewal(c)
	return h.actorForUserID(c, userID, ok, err)
}

func (h *Controller) actorForUserID(c fiber.Ctx, userID int64, ok bool, err error) (identity.Actor, error) {
	if err != nil {
		return identity.Actor{}, err
	}
	if !ok {
		return identity.Actor{}, fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}

	actor, err := h.service.Actor(c.Context(), userID)
	if err != nil {
		return identity.Actor{}, mapIdentityError(err)
	}
	// PAT：权限收窄到 scopes，避免令牌等价于完整 cookie 会话。
	if scopes := apitokens.ScopesFromContext(c.Context()); len(scopes) > 0 {
		actor = apitokens.RestrictActor(actor, scopes)
	}
	return actor, nil
}

func (h *Controller) auditLogin(
	ctx context.Context,
	c fiber.Ctx,
	userID int64,
	action string,
	sessionHash string,
) error {
	return h.service.RecordLoginAudit(ctx, identity.LoginAudit{
		UserID:      userID,
		Action:      action,
		IPAddress:   clientip.FromCtx(c),
		UserAgent:   c.Get(fiber.HeaderUserAgent),
		SessionHash: sessionHash,
	})
}

// applySessionDeviceInfo 解析请求 UA/IP 并设置到 pending 会话，Save 时写入会话目录。
// 真实 IP 全文进 ip_address，脱敏前缀进 ip_prefix（用户端设备列表）。
func (h *Controller) applySessionDeviceInfo(c fiber.Ctx, userID int64, pending *authsession.Pending) {
	if pending == nil {
		return
	}
	info := useragent.Parse(c.Get(fiber.HeaderUserAgent), clientip.FromCtx(c))
	pending.SetDeviceInfo(authsession.SessionRecordInput{
		UserID:       userID,
		DeviceName:   info.DeviceName,
		Browser:      info.Browser,
		OS:           info.OS,
		UserAgentRaw: info.UserAgentRaw,
		IPAddress:    info.IPAddress,
		IPPrefix:     info.IPPrefix,
	})
}

// enforceMaxSessions 读取 max_devices 配置并在登录后踢出超额设备。best-effort。
// currentSID 是本次登录的会话标识，一定不会被踢，保证刚登录的设备立即可用。
func (h *Controller) enforceMaxSessions(c fiber.Ctx, userID int64, currentSID string) {
	maxDevices := identity.RecommendedMaxDevices
	if h.options != nil {
		if raw, err := h.options.WebOption(c.Context(), identity.NameSessionsMaxDevices); err == nil && raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil {
				maxDevices = parsed
			}
		}
	}
	_, _ = h.service.EnforceMaxSessions(c.Context(), userID, currentSID, maxDevices)
}

// listSessions 返回当前用户的活跃设备列表（含 isCurrent 标记）。
// 自服务：actor 只能看自己的会话；越权由 store 层 user_id 过滤保证。
func (h *Controller) listSessions(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	currentSID, _ := h.authSessions.CurrentSID(c)
	// includeHistory=true 含已下线的历史记录；与 OpenAPI 契约一致，仅接受该参数名。
	includeHistory := c.Query("includeHistory") == "true"

	result, err := h.service.ListSessions(c.Context(), userID, currentSID, includeHistory, queryInt(c, "page"), queryInt(c, "perPage"))
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, result)
}

// revokeSession 下线当前用户的单个设备（由 path 传入 sid）。
// 越权保护：传别人的 sid 会因 store 层 user_id 不匹配返回 ErrSessionNotFound → 404，
// 不泄漏该 sid 是否属于他人。
func (h *Controller) revokeSession(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserIDWithoutRenewal(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	sid := strings.TrimSpace(c.Params("sessionId"))
	if sid == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if err := h.service.RevokeSession(c.Context(), userID, sid); err != nil {
		return mapIdentityError(err)
	}
	return apphttp.NoData(c)
}

// revokeOtherSessions 下线除当前设备外的所有其他设备。
func (h *Controller) revokeOtherSessions(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserIDWithoutRenewal(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	currentSID, _ := h.authSessions.CurrentSID(c)
	count, err := h.service.RevokeOtherSessions(c.Context(), userID, currentSID)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, map[string]any{"revoked": count})
}

// adminRevokeUserSessions 管理员强制下线目标用户的全部设备。
// 权限：user.manage；禁止对自己操作（下线自己请用 logout）。
// 鉴权在 service 层为权威，此处 actor 解析失败返回 401/403。
func (h *Controller) adminRevokeUserSessions(c fiber.Ctx) error {
	actor, err := h.actorWithoutSessionRenewal(c)
	if err != nil {
		return err
	}
	targetUserID, err := paramInt64(c, "userID")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	count, err := h.service.AdminRevokeUserSessions(c.Context(), actor, targetUserID)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, map[string]any{"revoked": count})
}

// adminSetUserPassword 管理员直接设置目标用户密码。
// 权限：user.manage；目标 super_admin 仅 super_admin 可操作。
// 成功后递增 token version 并撤销全部活跃会话（与公开密码重置一致）。
func (h *Controller) adminSetUserPassword(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	targetUserID, err := paramInt64(c, "userID")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	var input struct {
		Password string `json:"password"`
	}
	if err := c.Bind().Body(&input); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	result, err := h.service.AdminSetUserPassword(c.Context(), actor, targetUserID, input.Password)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, result)
}

// adminClearUserClientIPs 管理员清空目标用户相关真实客户端 IP（隐私合规）。
// 权限：user.manage。删号/封禁产品化后应在状态变更路径内自动调用。
func (h *Controller) adminClearUserClientIPs(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	targetUserID, err := paramInt64(c, "userID")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	result, err := h.service.ClearUserClientIPs(c.Context(), actor, targetUserID)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, result)
}
