package identitycontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	localization "github.com/keeptrue/sforum/apps/api/app/Support/Localization"
	useragent "github.com/keeptrue/sforum/apps/api/app/Support/UserAgent"
)

func (h *Controller) register(c fiber.Ctx) error {
	var req registerRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}

	input := identity.RegisterInput{
		Username:    req.Username,
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Locale:      req.Locale,
	}
	if err := h.service.ValidateRegister(c.Context(), input); err != nil {
		return mapIdentityError(err)
	}

	if err := h.verifier.Verify(c.Context(), humanverify.VerifyRequest{
		Provider: req.HumanVerification.Provider,
		Purpose:  humanverify.PurposeRegister,
		Token:    req.HumanVerification.Token,
		IP:       clientip.FromCtx(c),
	}); err != nil {
		return mapHumanVerificationError(err)
	}

	current, err := h.service.Register(c.Context(), input)
	if err != nil {
		return mapIdentityError(err)
	}
	h.queueWelcomeMail(c.Context(), req.Email, h.browserMailLocale(c), current)

	var pendingSession *authsession.Pending
	if err := h.runSessionIssue(c.Context(), current.ID, current.CurrentTokenVersion, req.StepUpEvidence, func(effectCtx context.Context) error {
		var beginErr error
		pendingSession, beginErr = h.beginSessionIssue(c, effectCtx, current.ID, current.CurrentTokenVersion)
		if beginErr != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		h.applySessionDeviceInfo(c, current.ID, pendingSession)
		if err := h.auditLogin(effectCtx, c, current.ID, identity.AuditActionRegister, pendingSession.Info().Hash); err != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		if err := pendingSession.SaveContext(effectCtx); err != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		return nil
	}); err != nil {
		return mapIdentityError(err)
	}
	// 登录成功后强制最大活跃设备数，best-effort 踢出最旧设备（失败不阻塞登录）。
	// 传入本次登录的 sid，确保当前设备永不被踢。
	h.enforceMaxSessions(c, current.ID, pendingSession.Info().SID)

	return apphttp.Created(c, current)
}

func (h *Controller) registrationStatus(c fiber.Ctx) error {
	status, err := h.service.RegistrationStatus(c.Context())
	if err != nil {
		return err
	}
	return apphttp.OK(c, status)
}

func (h *Controller) login(c fiber.Ctx) error {
	var req loginRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	// 令牌请求的形状错误在签发会话前返回，避免半成功状态。
	if err := h.validateLoginAPITokenRequest(req.IssueAPIToken); err != nil {
		return err
	}

	loginInput := identity.LoginInput{
		Login:    req.Login,
		Password: req.Password,
		ClientIP: clientip.FromCtx(c),
		// 与会话登记同口径的 UA 指纹：login_risk 的新设备判定依赖它。
		DeviceFingerprint: useragent.Parse(c.Get(fiber.HeaderUserAgent), clientip.FromCtx(c)).UserAgentRaw,
		// 登录同时签发 API Token 的请求豁免人机验证。这是已记录的取舍：
		// 该请求仍会签发浏览器会话，因此等同保留一处可绕过的验证豁免面。
		SkipHumanVerification: req.IssueAPIToken != nil,
	}
	current, err := h.service.Login(c.Context(), loginInput)
	if errors.Is(err, identity.ErrLoginVerificationRequired) {
		if verifyErr := h.verifier.Verify(c.Context(), humanverify.VerifyRequest{
			Provider: req.HumanVerification.Provider,
			Purpose:  humanverify.PurposeLoginRisk,
			Token:    req.HumanVerification.Token,
			IP:       loginInput.ClientIP,
		}); verifyErr != nil {
			return mapHumanVerificationError(verifyErr)
		}
		loginInput.HumanVerified = true
		current, err = h.service.Login(c.Context(), loginInput)
	}
	if err != nil {
		return mapIdentityError(err)
	}

	if err := h.runRiskEvaluation(c.Context(), current.ID, "login"); err != nil {
		return mapIdentityError(err)
	}

	var pendingSession *authsession.Pending
	if err := h.runSessionIssue(c.Context(), current.ID, current.CurrentTokenVersion, req.StepUpEvidence, func(effectCtx context.Context) error {
		var beginErr error
		pendingSession, beginErr = h.beginSessionIssue(c, effectCtx, current.ID, current.CurrentTokenVersion)
		if beginErr != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		h.applySessionDeviceInfo(c, current.ID, pendingSession)
		if err := h.auditLogin(effectCtx, c, current.ID, identity.AuditActionLogin, pendingSession.Info().Hash); err != nil {
			return err
		}
		return pendingSession.SaveContext(effectCtx)
	}); err != nil {
		return mapIdentityError(err)
	}
	// 登录成功后强制最大活跃设备数，best-effort 踢出最旧设备（失败不阻塞登录）。
	// 传入本次登录的 sid，确保当前设备永不被踢。
	h.enforceMaxSessions(c, current.ID, pendingSession.Info().SID)
	// T1D：成功密码登录后标记当前会话 recent-auth（敏感 unlink/password setup 前置）。
	if h.externalAuthService != nil {
		_ = h.externalAuthService.MarkSessionAuthenticated(
			c.Context(), current.ID, identity.SessionFingerprint(pendingSession.Info().SID),
			"password", "",
		)
	}

	// 原生/机器客户端可在同一次登录里拿到 PAT：省去 cookie 会话 + CSRF 的二次换取。
	issued, err := h.issueLoginAPIToken(c, current.ID, req.IssueAPIToken)
	if err != nil {
		return err
	}
	if issued != nil {
		return apphttp.OK(c, loginIssuedAPIToken{User: current, APIToken: *issued})
	}

	return apphttp.OK(c, current)
}

func (h *Controller) humanVerificationChallenge(c fiber.Ctx) error {
	purpose, ok := parseHumanVerificationPurpose(c.Query("purpose"))
	if !ok {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}

	challenge, err := h.verifier.Challenge(c.Context(), purpose, humanverify.Subject{IP: clientip.FromCtx(c)})
	if err != nil {
		return mapHumanVerificationError(err)
	}
	// ALTCHA widget 直接消费该端点，成功响应必须保持 ALTCHA 原始协议形状。
	return c.Status(fiber.StatusOK).JSON(challenge.Payload)
}

func (h *Controller) logout(c fiber.Ctx) error {
	if err := h.authSessions.Destroy(c); err != nil {
		return err
	}
	return apphttp.NoData(c)
}

func (h *Controller) session(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}

	current, err := h.service.CurrentUser(c.Context(), userID)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, current)
}

func (h *Controller) updateCurrentUserLocale(c fiber.Ctx) error {
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	var req struct {
		Locale string `json:"locale"`
	}
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	locale, valid := h.resolveUserLocale(c.Context(), req.Locale)
	if !valid {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	current, err := h.localePreferences.Update(c.Context(), userID, locale)
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, current)
}

func (h *Controller) resolveUserLocale(ctx context.Context, requested string) (string, bool) {
	supported := localization.ParseSupportedLocales("")
	defaultLocale := localization.DefaultLocale
	if h.options != nil {
		if value, err := h.options.WebOption(ctx, "site.supported_locales"); err == nil {
			supported = localization.ParseSupportedLocales(value)
		}
		if value, err := h.options.WebOption(ctx, "site.default_locale"); err == nil {
			defaultLocale = localization.Normalize(value, supported)
		}
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return defaultLocale, true
	}
	canonical := localization.Normalize(requested, nil)
	for _, candidate := range supported {
		if canonical == candidate {
			return candidate, true
		}
	}
	return "", false
}

// passwordResetRequest 发起密码重置。响应始终为成功，不暴露邮箱是否存在。
func (h *Controller) passwordResetRequest(c fiber.Ctx) error {
	if h.passwordReset == nil {
		// 未配置密码重置服务时返回通用成功，避免暴露能力差异。
		return apphttp.OK(c, map[string]any{"sent": true})
	}
	var req passwordResetRequestPayload
	_ = c.Bind().Body(&req)
	// 可选人机验证：当 runtime options 启用 password_reset purpose 时校验。
	// 直接读取首次绑定的 req.HumanVerification，避免对同一 body 二次绑定导致嵌套 token 丢失。
	if err := h.verifier.Verify(c.Context(), humanverify.VerifyRequest{
		Provider: req.HumanVerification.Provider,
		Token:    req.HumanVerification.Token,
		Purpose:  humanverify.PurposePasswordReset,
		IP:       clientip.FromCtx(c),
	}); err != nil {
		return mapHumanVerificationError(err)
	}
	ip := clientip.FromCtx(c)
	result, requestErr := h.passwordReset.RequestPasswordResetWithResult(c.Context(), identity.RequestPasswordResetInput{
		// 规范化邮箱：trim 后传给 service，与 register/login 路径的输入处理保持一致。
		Email:  strings.TrimSpace(req.Email),
		IP:     ip,
		Locale: h.browserMailLocale(c),
	})
	if errors.Is(requestErr, identity.ErrPasswordResetRateLimited) {
		return mapIdentityError(requestErr)
	}
	return apphttp.OK(c, mailRequestResponse(true, result.RetryAt))
}

func (h *Controller) browserMailLocale(c fiber.Ctx) string {
	if strings.TrimSpace(c.Get("Accept-Language")) == "" {
		return ""
	}
	locale, valid := h.resolveUserLocale(c.Context(), c.Get("Accept-Language"))
	if !valid {
		return ""
	}
	return locale
}

// passwordResetConfirm 校验令牌并更新密码。
func (h *Controller) passwordResetConfirm(c fiber.Ctx) error {
	if h.passwordReset == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.password_reset_unavailable")
	}
	var req passwordResetConfirmPayload
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.password_reset_invalid")
	}
	if err := h.passwordReset.ConfirmPasswordReset(c.Context(), identity.ConfirmPasswordResetInput{
		Token:       req.Token,
		NewPassword: req.NewPassword,
	}); err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, map[string]any{"reset": true})
}

func (h *Controller) emailVerificationRequest(c fiber.Ctx) error {
	if h.emailVerification == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.email_verification_unavailable")
	}
	userID, ok, err := h.sessionUserID(c)
	if err != nil {
		return err
	}
	if !ok || userID <= 0 {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.required")
	}
	var req emailVerificationRequest
	if len(c.Body()) > 0 {
		if err := c.Bind().Body(&req); err != nil {
			return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
		}
	}
	if err := h.verifier.Verify(c.Context(), humanverify.VerifyRequest{
		Provider: req.HumanVerification.Provider,
		Purpose:  humanverify.PurposeEmailVerification,
		Token:    req.HumanVerification.Token,
		IP:       clientip.FromCtx(c),
		UserID:   &userID,
	}); err != nil {
		return mapHumanVerificationError(err)
	}
	result, err := h.emailVerification.RequestWithResult(c.Context(), userID, clientip.FromCtx(c), h.browserMailLocale(c))
	if err != nil {
		return mapIdentityError(err)
	}
	return apphttp.OK(c, mailRequestResponse(result.Sent, result.RetryAt))
}

func mailRequestResponse(sent bool, retryAt time.Time) map[string]any {
	data := map[string]any{"sent": sent}
	if retryAt.IsZero() {
		return data
	}
	retryAt = retryAt.UTC()
	data["retryAfterSeconds"] = max(1, int(math.Ceil(time.Until(retryAt).Seconds())))
	data["retryAt"] = retryAt
	return data
}

func (h *Controller) emailVerificationConfirm(c fiber.Ctx) error {
	if h.emailVerification == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.email_verification_unavailable")
	}
	token := strings.TrimSpace(c.Query("token"))
	if token == "" {
		var req struct {
			Token string `json:"token"`
		}
		if err := c.Bind().Body(&req); err == nil {
			token = strings.TrimSpace(req.Token)
		}
	}
	if _, err := h.emailVerification.Confirm(c.Context(), token); err != nil {
		return mapIdentityError(err)
	}
	if c.Method() == fiber.MethodGet {
		if _, ok, _ := h.sessionUserIDWithoutRenewal(c); ok {
			return c.Redirect().Status(fiber.StatusSeeOther).To("/email-verification?verified=1")
		}
		return c.Redirect().Status(fiber.StatusSeeOther).To("/login?reason=auth.email_verified")
	}
	return apphttp.OK(c, map[string]any{"verified": true})
}

// adminMailTest 向指定收件人或站点管理员邮箱发送测试邮件。
// 收件人优先级：请求 body.recipient → site.admin_email → 422。
func (h *Controller) adminMailTest(c fiber.Ctx) error {
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	if !actor.Can(identity.PermissionSettingsMailManage) {
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	}
	if h.mailQueue == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "mail.unavailable")
	}
	var req mailTestRequest
	_ = c.Bind().Body(&req)
	adminEmail := ""
	if h.options != nil {
		if value, err := h.options.AdminEmail(c.Context()); err == nil {
			adminEmail = value
		}
	}
	// 收件人优先级：请求 body → site.admin_email（与 SMTP From 无关）。
	recipient, validRecipient := resolveMailTestRecipient(req.Recipient, adminEmail)
	if !validRecipient {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "mail.test_recipient_required")
	}
	siteName := "SForum"
	if h.options != nil {
		if name, err := h.options.SiteName(c.Context()); err == nil && name != "" {
			siteName = name
		}
	}
	data, _ := json.Marshal(map[string]string{"subject": "[" + siteName + "] 测试邮件 / Test mail", "textBody": "这是一封来自 " + siteName + " 的测试邮件，用于验证邮件投递配置是否生效。"})
	delivery, err := h.mailQueue.QueueMail(c.Context(), notifications.QueueMailInput{Recipient: recipient, TemplateKey: "admin.test", TemplateData: data, IdempotencyKey: fmt.Sprintf("admin_test:%d:%d", actor.ID, time.Now().UnixNano())})
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "mail.test_failed")
	}
	return apphttp.JSON(c, fiber.StatusAccepted, apphttp.MessageOK, map[string]any{"queued": true, "deliveryId": delivery.ID, "recipient": recipient})
}

func normalizeTestRecipient(value string) (string, bool) {
	value = strings.TrimSpace(value)
	address, err := mail.ParseAddress(value)
	if err != nil || address.Address != value {
		return "", false
	}
	return value, true
}

// resolveMailTestRecipient 解析测试邮件收件人：显式 recipient 优先，否则 site.admin_email。
func resolveMailTestRecipient(explicit, adminEmail string) (string, bool) {
	if recipient, ok := normalizeTestRecipient(explicit); ok {
		return recipient, true
	}
	return normalizeTestRecipient(adminEmail)
}

type passwordResetRequestPayload struct {
	Email             string                   `json:"email"`
	HumanVerification humanVerificationRequest `json:"humanVerification"`
}

type passwordResetConfirmPayload struct {
	Token       string `json:"token"`
	NewPassword string `json:"newPassword"`
}

type mailTestRequest struct {
	Recipient string `json:"recipient"`
}
