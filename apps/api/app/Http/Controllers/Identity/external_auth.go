package identitycontroller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
)

// 外部认证 Host 端点：保留 Core 回调路由 + 登录/注册/link 编排。
//
// 安全约束（见 plans/2026-07-27-github-social-login-builtin-plugin.md）：
//   - OAuth 回调使用保留 Core GET 路由（不在 Route Registry 内）；
//   - 回调事务一次性消费；跨 provider/operation/actor/artifact 一律 fail closed；
//   - 浏览器永远不接收 raw subject/digest/code/token/state/verifier/secret；
//   - 注册需要不透明一次性票据；
//   - 敏感操作（unlink/密码设置）需要最近认证。

const (
	externalAuthSafeReturnMaxLen = 2000
)

// externalAuthCallback 处理 OAuth provider 浏览器重定向回来的 GET 回调。
// 失败一律 302 到安全本地路径并带最小化安全提示参数；绝不返回 raw 内部值。
//
// T1A 授权顺序（任一失败零写入）：
//  1. 原子消费 callback 事务；
//  2. 重新解析 live Registry provider，比对 provider/operation/owner/version/digest；
//  3. 重新校验 Host 有效激活；
//  4. link：当前会话 actor + recent-auth 必须在插件 complete 与 link 持久化之前通过；
//  5. 将 Host 保存的 PKCE verifier 与同一绝对 callback URL 传入 complete；
//  6. 仅在断言成功后执行 login/registration ticket/link 业务效应。
func (h *Controller) externalAuthCallback(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.callbackStateStore == nil || h.authFlow == nil {
		return externalAuthRedirect(c, "/login", "auth.provider_unavailable")
	}
	// M5：callback 为 GET，不受写限流保护；专用 IP 限流防 state 爆破。
	// 超限仍走安全 redirect；reason 必须是 auth.* 稳定码（前端 ext_auth 白名单）。
	if !h.allowExternalAuthRate(c, "callback", identity.ExternalAuthCallbackMaxPerIP) {
		return externalAuthRedirect(c, "/login", "auth.provider_unavailable")
	}
	providerID := strings.ToLower(strings.TrimSpace(c.Params("providerId")))
	if providerID == "" {
		return externalAuthRedirect(c, "/login", "auth.provider_not_found")
	}
	rawState := strings.TrimSpace(c.Query("state"))
	code := strings.TrimSpace(c.Query("code"))
	// 取消授权时返回 ?error=access_denied，不带 code/state。
	if rawState == "" || code == "" {
		if errParam := strings.TrimSpace(c.Query("error")); errParam != "" {
			return externalAuthRedirect(c, "/login", "auth.provider_cancelled")
		}
		return externalAuthRedirect(c, "/login", "auth.provider_callback_invalid")
	}

	// 1. 原子一次性消费回调事务。
	tx, err := h.callbackStateStore.Consume(c.Context(), rawState)
	if err != nil {
		return externalAuthRedirect(c, "/login", mapCallbackStateError(err))
	}
	returnPath := txRedirect(tx)
	if tx.BrowserBindingDigest != "" && !h.matchesExternalAuthBrowserBinding(c, tx.BrowserBindingDigest) {
		return externalAuthRedirect(c, returnPath, "auth.provider_callback_invalid")
	}

	// 2–3. live artifact + 有效激活（在任何插件调用 / 业务效应之前）。
	live, err := h.externalAuthService.ValidateCallbackBeforeEffect(c.Context(), tx, providerID)
	if err != nil {
		return externalAuthRedirect(c, returnPath, mapExternalAuthReason(err))
	}

	// 4. link 操作：会话 actor + session-bound recent-auth 必须先于 complete/persist。
	var sessionUserID int64
	var sessionFingerprint string
	if tx.Operation == identity.ExternalAuthOperationLink {
		actor, actorErr := h.actor(c)
		if actorErr != nil {
			return externalAuthRedirect(c, returnPath, "auth.required")
		}
		sessionUserID = actor.ID
		sessionFingerprint = h.currentSessionFingerprint(c)
		if err := h.externalAuthService.AuthorizeLinkBeforePersist(c.Context(), tx, sessionUserID, sessionFingerprint); err != nil {
			return externalAuthRedirect(c, returnPath, mapExternalAuthReason(err))
		}
	}

	// 5. 调用插件 complete：传入 Host 保存的 PKCE verifier 与同一绝对 callback URL。
	completeOp := identity.AuthOperationLoginComplete
	switch tx.Operation {
	case identity.ExternalAuthOperationRegistration:
		completeOp = identity.AuthOperationRegistrationComplete
	case identity.ExternalAuthOperationLink:
		completeOp = identity.AuthOperationLinkComplete
	}
	// 绝对 callback：优先事务内绑定值；缺失时按 site.url -> APP_URL 重建。
	callbackURL := strings.TrimSpace(tx.AbsoluteCallbackURL)
	if callbackURL == "" {
		callbackURL, err = h.absoluteCallbackURL(c.Context(), providerID)
		if err != nil {
			return externalAuthRedirect(c, returnPath, "auth.provider_unavailable")
		}
	}
	flowResult, err := h.authFlow.Complete(c.Context(), identity.AuthProviderCompleteInput{
		ProviderID:        providerID,
		Operation:         completeOp,
		ActorUserID:       tx.ActorUserID,
		TargetUserID:      tx.ActorUserID,
		CorrelationID:     tx.CorrelationID,
		CompletionToken:   code,
		DeviceFingerprint: tx.DeviceFingerprint,
		ClientClass:       tx.ClientClass,
		IdempotencyKey:    "callback:" + tx.State,
		CodeVerifier:      tx.CodeVerifier,
		CallbackURL:       callbackURL,
	})
	if err != nil {
		return externalAuthRedirect(c, returnPath, mapAuthProviderCompleteCallback(err))
	}
	// complete 返回的 live artifact 必须与事务一致（纵深）。
	if flowResult.OwnerPackageDigest != "" && flowResult.OwnerPackageDigest != tx.OwnerPackageDigest {
		return externalAuthRedirect(c, returnPath, "auth.provider_callback_invalid")
	}
	if flowResult.OwnerExtensionID != "" && flowResult.OwnerExtensionID != tx.OwnerExtensionID {
		return externalAuthRedirect(c, returnPath, "auth.provider_callback_invalid")
	}
	_ = live

	// 6. 构造断言（raw subject 只在此结构内短暂存在）。
	// Owner* 优先事务绑定；complete 返回的版本信息一并保留供 ticket/提交比对。
	ownerVersion := strings.TrimSpace(tx.OwnerExtensionVersion)
	if ownerVersion == "" {
		ownerVersion = strings.TrimSpace(flowResult.OwnerExtensionVersion)
	}
	assertion := identity.ExternalAuthAssertion{
		ProviderID:              providerID,
		ProviderContractVersion: flowResult.ProviderContractVersion,
		OwnerExtensionID:        tx.OwnerExtensionID,
		OwnerExtensionVersion:   ownerVersion,
		OwnerPackageDigest:      tx.OwnerPackageDigest,
		Operation:               tx.Operation,
		ProviderSubject:         flowResult.ProviderSubject,
		SubjectDigest:           flowResult.SubjectDigest, // 兼容旧 fixture 路径
		UsernameHint:            flowResult.UsernameHint,
		DisplayName:             flowResult.DisplayName,
		EmailHint:               flowResult.EmailHint,
		EmailVerified:           flowResult.EmailVerified,
		CorrelationID:           tx.CorrelationID,
	}
	// Provider complete only proves an external assertion. Re-check the current
	// Host authorization before account resolution; the session effect performs
	// the same check again at its own admission boundary.
	if tx.Operation == identity.ExternalAuthOperationLogin {
		if err := h.externalAuthService.ValidateLoginEffect(c.Context(), assertion); err != nil {
			return externalAuthRedirect(c, returnPath, mapExternalAuthReason(err))
		}
	}

	switch tx.Operation {
	case identity.ExternalAuthOperationLogin:
		return h.handleExternalLoginCallback(c, tx, assertion, returnPath)
	case identity.ExternalAuthOperationRegistration:
		return h.handleExternalRegistrationCallback(c, tx, assertion, returnPath)
	case identity.ExternalAuthOperationLink:
		return h.handleExternalLinkCallback(c, tx, assertion, returnPath, sessionUserID, sessionFingerprint)
	default:
		return externalAuthRedirect(c, returnPath, "auth.provider_callback_invalid")
	}
}

// handleExternalLoginCallback 登录：账号解析 + 风险评估 + 会话签发 + 审计。
func (h *Controller) handleExternalLoginCallback(c fiber.Ctx, tx identity.CallbackTransaction, assertion identity.ExternalAuthAssertion, returnPath string) error {
	result, err := h.externalAuthService.CompleteLogin(c.Context(), assertion)
	if err != nil {
		if errors.Is(err, identity.ErrExternalIdentityUnlinked) {
			// 未绑定且 registration 仍有效开放时，复用同一个 Host 注册 continuation。
			target, continuationErr := h.createExternalIdentityContinuation(c, assertion, returnPath, tx.BrowserBindingDigest)
			if continuationErr == nil {
				return c.Redirect().Status(fiber.StatusFound).To(target)
			}
			return externalAuthRedirect(c, "/login", mapRegistrationContinuationReason(continuationErr))
		}
		return externalAuthRedirect(c, returnPath, mapExternalAuthReason(err))
	}
	// 风险评估。
	if err := h.runRiskEvaluation(c.Context(), result.User.ID, "login"); err != nil {
		return externalAuthRedirect(c, returnPath, mapIdentityErrorReason(err))
	}
	// 会话签发：复用密码登录的 runSessionIssue + beginSessionIssue 模式。
	var pendingSession *authsession.Pending
	if err := h.runSessionIssue(c.Context(), result.User.ID, result.User.CurrentTokenVersion, "", func(effectCtx context.Context) error {
		// This is the external-login effect fence. It runs after risk evaluation
		// and immediately before any pending browser session is created or saved.
		if err := h.externalAuthService.ValidateLoginEffect(effectCtx, assertion); err != nil {
			return err
		}
		var beginErr error
		pendingSession, beginErr = h.beginSessionIssue(c, effectCtx, result.User.ID, result.User.CurrentTokenVersion)
		if beginErr != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		h.applySessionDeviceInfo(c, result.User.ID, pendingSession)
		if err := h.auditLogin(effectCtx, c, result.User.ID, identity.AuditActionExternalLogin, pendingSession.Info().Hash); err != nil {
			return err
		}
		return pendingSession.SaveContext(effectCtx)
	}); err != nil {
		return externalAuthRedirect(c, returnPath, mapIdentityErrorReason(err))
	}
	h.enforceMaxSessions(c, result.User.ID, pendingSession.Info().SID)
	// T1D：成功外部认证后标记当前会话的 recent-auth（跨 session 隔离）。
	_ = h.externalAuthService.MarkSessionAuthenticated(
		c.Context(), result.User.ID, identity.SessionFingerprint(pendingSession.Info().SID),
		"external", assertion.ProviderID,
	)
	return externalAuthRedirect(c, returnPath, "auth.external_login_ok")
}

// handleExternalRegistrationCallback 生成不透明一次性注册票据，302 到固定 Host 注册路由。
// 浏览器只看到 ticket 字符串；raw subject 只存在票据内部（Redis）。
// safe redirect 作为独立 query，不得改写注册 continuation 路由本身。
func (h *Controller) handleExternalRegistrationCallback(c fiber.Ctx, tx identity.CallbackTransaction, assertion identity.ExternalAuthAssertion, returnPath string) error {
	target, err := h.createExternalIdentityContinuation(c, assertion, returnPath, tx.BrowserBindingDigest)
	if err != nil {
		return externalAuthRedirect(c, returnPath, mapRegistrationContinuationReason(err))
	}
	return c.Redirect().Status(fiber.StatusFound).To(target)
}

// handleExternalLinkCallback 在已通过 actor/recent-auth 门控后持久化 link。
// sessionUserID 必须与事务 actor 一致（由 AuthorizeLinkBeforePersist 保证）。
func (h *Controller) handleExternalLinkCallback(
	c fiber.Ctx,
	tx identity.CallbackTransaction,
	assertion identity.ExternalAuthAssertion,
	returnPath string,
	sessionUserID int64,
	sessionFingerprint string,
) error {
	if sessionUserID == 0 || sessionUserID != tx.ActorUserID {
		return externalAuthRedirect(c, returnPath, "auth.provider_callback_invalid")
	}
	if _, err := h.externalAuthService.CompleteLink(c.Context(), assertion, sessionUserID, sessionFingerprint); err != nil {
		return externalAuthRedirect(c, "/settings/security/login-methods", mapExternalAuthReason(err))
	}
	return externalAuthRedirect(c, "/settings/security/login-methods", "auth.external_link_ok")
}

// externalRegistration 用一次性票据原子创建用户 + 默认角色 + link。
// T1D：可编辑字段与人机验证在消费 opaque ticket 之前完成，允许修正而不重放断言。
func (h *Controller) externalRegistration(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.registrationTicketStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	var req externalRegistrationRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	preview, err := h.registrationTicketStore.Inspect(c.Context(), strings.TrimSpace(req.Ticket))
	if err != nil {
		return mapRegistrationTicketError(err)
	}
	if !h.matchesExternalAuthTicketBrowserBinding(c, preview) {
		return mapRegistrationTicketError(identity.ErrRegistrationTicketInvalid)
	}
	input := identity.ExternalRegistrationInput{
		Username:    strings.TrimSpace(req.Username),
		Email:       strings.TrimSpace(req.Email),
		DisplayName: strings.TrimSpace(req.DisplayName),
		Locale:      strings.TrimSpace(req.Locale),
	}.Normalized()
	// 1. 权威字段/策略校验（消费 ticket 前，便于修正后重试）。
	if h.service != nil {
		if err := h.service.ValidateExternalRegister(c.Context(), input); err != nil {
			return mapExternalAuthRegistrationError(err)
		}
	}
	// 2. 人机验证（与密码注册同一 purpose）。
	if err := h.verifier.Verify(c.Context(), humanverify.VerifyRequest{
		Provider: req.HumanVerification.Provider,
		Purpose:  humanverify.PurposeRegister,
		Token:    req.HumanVerification.Token,
		IP:       clientip.FromCtx(c),
	}); err != nil {
		return mapHumanVerificationError(err)
	}
	// 3. 原子消费 opaque ticket（此后失败需重新 OAuth）。
	ticket, err := h.registrationTicketStore.Consume(c.Context(), strings.TrimSpace(req.Ticket))
	if err != nil {
		return mapRegistrationTicketError(err)
	}
	if !h.matchesExternalAuthTicketBrowserBinding(c, ticket) {
		return mapRegistrationTicketError(identity.ErrRegistrationTicketInvalid)
	}
	assertion := externalAuthAssertionFromTicket(ticket)
	assertion.Operation = identity.ExternalAuthOperationRegistration
	result, err := h.externalAuthService.CompleteRegistration(c.Context(), assertion, input)
	if err != nil {
		return mapExternalAuthRegistrationError(err)
	}
	h.queueWelcomeMail(c.Context(), input.Email, h.browserMailLocale(c), result.User)
	// 注册成功后建立会话（与密码注册一致；result.User 已走权威 CurrentUser）。
	if err := h.runRiskEvaluation(c.Context(), result.User.ID, "register"); err != nil {
		return mapIdentityError(err)
	}
	var pendingSession *authsession.Pending
	if err := h.runSessionIssue(c.Context(), result.User.ID, result.User.CurrentTokenVersion, "", func(effectCtx context.Context) error {
		var beginErr error
		pendingSession, beginErr = h.beginSessionIssue(c, effectCtx, result.User.ID, result.User.CurrentTokenVersion)
		if beginErr != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
		}
		h.applySessionDeviceInfo(c, result.User.ID, pendingSession)
		if err := h.auditLogin(effectCtx, c, result.User.ID, identity.AuditActionExternalRegister, pendingSession.Info().Hash); err != nil {
			return err
		}
		return pendingSession.SaveContext(effectCtx)
	}); err != nil {
		return mapIdentityError(err)
	}
	h.enforceMaxSessions(c, result.User.ID, pendingSession.Info().SID)
	_ = h.externalAuthService.MarkSessionAuthenticated(
		c.Context(), result.User.ID, identity.SessionFingerprint(pendingSession.Info().SID),
		"external", assertion.ProviderID,
	)
	return apphttp.Created(c, result.User)
}

// externalIdentities 列出当前用户的已绑定身份（redacted，无 raw subject/digest）。
func (h *Controller) externalIdentities(c fiber.Ctx) error {
	if h.externalLinkStore == nil {
		return apphttp.OK(c, []externalIdentityItem{})
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	links, err := h.externalLinkStore.ListUser(c.Context(), actor.ID)
	if err != nil {
		return mapAuthProviderHTTPError(err)
	}
	items := make([]externalIdentityItem, 0, len(links))
	for _, link := range links {
		items = append(items, externalIdentityItem{
			LinkID:           link.ID,
			ProviderID:       link.ProviderID,
			Status:           link.Status,
			LinkedAt:         link.LinkedAt,
			OwnerExtensionID: link.OwnerExtensionID,
		})
	}
	return apphttp.OK(c, items)
}

// externalIdentityUnlink 解绑一个外部身份。
// T1D：session-bound recent-auth；加载 link 校验所有权/active/revision；
// last-method 与 unlink 同事务；idempotency 绑定 user/link/revision/request（非 IP）。
func (h *Controller) externalIdentityUnlink(c fiber.Ctx) error {
	if h.externalAuthService == nil || h.externalLinkStore == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	linkID, err := paramInt64(c, "linkId")
	if err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	// 加载目标 link：所有权 / active / expected revision。
	link, err := h.externalLinkStore.Get(c.Context(), linkID)
	if err != nil {
		return mapAuthProviderHTTPError(err)
	}
	if link.UserID != actor.ID || link.Status != identity.ExternalIdentityLinkStatusActive {
		return fiber.NewError(fiber.StatusNotFound, "auth.external_identity_not_found")
	}
	// 可选 body：expectedRevision / requestId（缺省用当前 revision 与一次性 request）。
	var body externalUnlinkRequest
	_ = c.Bind().Body(&body)
	expectedRevision := body.ExpectedRevision
	if expectedRevision <= 0 {
		expectedRevision = link.Revision
	}
	requestID := strings.TrimSpace(body.RequestID)
	if requestID == "" {
		// 不使用 client IP；用 opaque 一次性后缀保证非 IP 绑定。
		if opaque, genErr := identity.GenerateOpaqueToken(); genErr == nil {
			requestID = opaque
		} else {
			requestID = fmt.Sprintf("req-%d", time.Now().UnixNano())
		}
	}
	if _, err := h.externalAuthService.Unlink(c.Context(), identity.UnlinkExternalIdentityInput{
		UserID:             actor.ID,
		LinkID:             linkID,
		ExpectedRevision:   expectedRevision,
		SessionFingerprint: h.currentSessionFingerprint(c),
		RequestID:          requestID,
	}); err != nil {
		return mapExternalAuthError(err)
	}
	return apphttp.NoData(c)
}

// setupPassword 自助添加/更改本地密码（M1 POST /auth/password）。
// 需要 session-bound recent-auth；external-only 用户在缺失时创建 credential 行。
func (h *Controller) setupPassword(c fiber.Ctx) error {
	if h.service == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	// session-bound recent-auth（与 unlink 同一门控；无 store 时 fail closed）。
	fp := h.currentSessionFingerprint(c)
	if h.externalAuthStore == nil {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.recent_auth_required")
	}
	recent, recentErr := h.externalAuthStore.IsSessionRecentlyAuthenticated(c.Context(), actor.ID, fp)
	if recentErr != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	if !recent {
		return fiber.NewError(fiber.StatusUnauthorized, "auth.recent_auth_required")
	}
	var req setupPasswordRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "validation.invalid")
	}
	if err := h.service.SetupPassword(c.Context(), actor.ID, req.Password); err != nil {
		return mapIdentityError(err)
	}
	return apphttp.NoData(c)
}

// currentSessionFingerprint 返回当前浏览器会话的不可逆 SID fingerprint。
func (h *Controller) currentSessionFingerprint(c fiber.Ctx) string {
	if h.authSessions == nil {
		return ""
	}
	sid, err := h.authSessions.CurrentSID(c)
	if err != nil || sid == "" {
		return ""
	}
	return identity.SessionFingerprint(sid)
}

// --- 请求/响应类型 ---

type externalRegistrationRequest struct {
	Ticket            string                   `json:"ticket"`
	Username          string                   `json:"username"`
	Email             string                   `json:"email"`
	DisplayName       string                   `json:"displayName"`
	Locale            string                   `json:"locale"`
	HumanVerification humanVerificationRequest `json:"humanVerification"`
}

type externalUnlinkRequest struct {
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

type setupPasswordRequest struct {
	Password string `json:"password"`
}

type externalIdentityItem struct {
	LinkID           int64       `json:"linkId"`
	ProviderID       string      `json:"providerId"`
	Status           string      `json:"status"`
	LinkedAt         interface{} `json:"linkedAt"`
	OwnerExtensionID string      `json:"ownerExtensionId"`
}

// --- 安全重定向与错误映射 ---

// externalAuthRedirect 安全 302 到 safeReturn；携带最小化安全提示。
// 路径必须经过 externalSafeReturnPath 校验，防止 open redirect。
func externalAuthRedirect(c fiber.Ctx, safeReturn, reason string) error {
	target := safeReturn
	if !externalSafeReturnPath(target) {
		target = "/login"
	}
	u, err := url.Parse(target)
	if err != nil {
		u, _ = url.Parse("/login")
	}
	q := u.Query()
	if reason != "" {
		q.Set("ext_auth", reason)
	}
	u.RawQuery = q.Encode()
	return c.Redirect().Status(fiber.StatusFound).To(u.String())
}

func txRedirect(tx identity.CallbackTransaction) string {
	if externalSafeReturnPath(tx.RedirectPath) {
		return tx.RedirectPath
	}
	return "/login"
}

// externalSafeReturnPath 校验路径是站内绝对路径、非 /api/、非外部。
// 登录/注册循环目的地在 auth return 场景下拒绝，避免回调后再次落到入口页。
func externalSafeReturnPath(path string) bool {
	if !identity.ValidateSafeRedirectPath(path) {
		return false
	}
	if len(path) > externalAuthSafeReturnMaxLen {
		return false
	}
	// 登录/注册入口不作为 post-auth return（注册 continuation 使用固定 Host 路由）。
	if path == "/login" || path == "/register" || strings.HasPrefix(path, "/login?") || strings.HasPrefix(path, "/register?") {
		return false
	}
	return true
}

func mapCallbackStateError(err error) string {
	switch {
	case errors.Is(err, identity.ErrCallbackStateExpired):
		return "auth.provider_callback_expired"
	case errors.Is(err, identity.ErrCallbackStateReplayed):
		return "auth.provider_callback_replayed"
	default:
		return "auth.provider_callback_invalid"
	}
}

func mapAuthProviderCompleteCallback(err error) string {
	// 回调失败一律走安全 reason，绝不回显 raw 内部错误（可能含 code/token）。
	_ = err
	return "auth.provider_unavailable"
}

func mapExternalAuthReason(err error) string {
	switch {
	case errors.Is(err, identity.ErrExternalIdentitySubjectConflict):
		return "auth.external_subject_conflict"
	case errors.Is(err, identity.ErrExternalAuthActorInactive),
		errors.Is(err, identity.ErrExternalAuthActorRequired),
		errors.Is(err, identity.ErrExternalAuthActorMismatch):
		return "auth.required"
	case errors.Is(err, identity.ErrExternalAuthRecentAuthRequired):
		return "auth.recent_auth_required"
	case errors.Is(err, identity.ErrExternalAuthOperationNotActivated):
		return "auth.provider_not_enabled"
	case errors.Is(err, identity.ErrExternalAuthArtifactMismatch),
		errors.Is(err, identity.ErrCallbackStateInvalid):
		return "auth.provider_callback_invalid"
	case errors.Is(err, identity.ErrExternalAuthProviderUnavailable):
		return "auth.provider_unavailable"
	default:
		return "auth.provider_unavailable"
	}
}

func mapExternalAuthError(err error) error {
	switch {
	case errors.Is(err, identity.ErrExternalIdentitySubjectConflict):
		return fiber.NewError(fiber.StatusConflict, "auth.external_subject_conflict")
	case errors.Is(err, identity.ErrExternalAuthLastLoginMethodRequired):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.last_login_method_required")
	case errors.Is(err, identity.ErrExternalAuthRecentAuthRequired):
		return fiber.NewError(fiber.StatusUnauthorized, "auth.recent_auth_required")
	case errors.Is(err, identity.ErrExternalIdentityLinkNotFound):
		return fiber.NewError(fiber.StatusNotFound, "auth.external_identity_not_found")
	case errors.Is(err, identity.ErrExternalIdentityLinkStateConflict):
		return fiber.NewError(fiber.StatusConflict, "auth.external_link_conflict")
	case errors.Is(err, identity.ErrExternalAuthActorRequired), errors.Is(err, identity.ErrExternalAuthActorInactive):
		return fiber.NewError(fiber.StatusForbidden, "auth.required")
	default:
		// RegisterInvalidError 等字段错误由 mapIdentityError 处理更合适。
		var reg *identity.RegisterInvalidError
		if errors.As(err, &reg) {
			return mapIdentityError(err)
		}
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
}

func mapExternalAuthRegistrationError(err error) error {
	switch {
	case errors.Is(err, identity.ErrExternalRegistrationFieldUsername),
		errors.Is(err, identity.ErrExternalRegistrationFieldEmail):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.register_invalid")
	case errors.Is(err, identity.ErrExternalAuthBootstrapRequired):
		return fiber.NewError(fiber.StatusForbidden, "auth.external_bootstrap_required")
	case errors.Is(err, identity.ErrRegistrationDisabled):
		return fiber.NewError(fiber.StatusForbidden, "auth.registration_disabled")
	case errors.Is(err, identity.ErrExternalAuthDefaultRoleFailed):
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	default:
		var reg *identity.RegisterInvalidError
		if errors.As(err, &reg) {
			return mapIdentityError(err)
		}
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
}

func mapRegistrationTicketError(err error) error {
	switch {
	case errors.Is(err, identity.ErrRegistrationTicketExpired):
		return fiber.NewError(fiber.StatusGone, "auth.external_registration_ticket_expired")
	default:
		return fiber.NewError(fiber.StatusNotFound, "auth.external_registration_ticket_invalid")
	}
}

func mapIdentityErrorReason(err error) string {
	var fe *fiber.Error
	if errors.As(err, &fe) {
		return fe.Message
	}
	return "auth.provider_unavailable"
}
