package identitycontroller

import (
	"errors"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

// mapAuthStartToExternalOperation 把内部 AuthOperation*Start 映射到外部 Host 操作枚举。
func mapAuthStartToExternalOperation(op string) (identity.ExternalAuthOperation, error) {
	switch op {
	case identity.AuthOperationLoginStart:
		return identity.ExternalAuthOperationLogin, nil
	case identity.AuthOperationRegistrationStart:
		return identity.ExternalAuthOperationRegistration, nil
	case identity.AuthOperationLinkStart:
		return identity.ExternalAuthOperationLink, nil
	default:
		return "", errors.New("not an external auth start operation")
	}
}

// listAuthProviders 返回有效可用 auth/recovery 提供方的 redacted 列表。
// 不含 artifact 路径、Schema 摘要、raw subject 或 handler 内部细节。
// T1C：仅有效可用 provider；Host 激活目录查询失败 fail closed（不得静默部分暴露）。
// 展示信息全部来自 Host catalog，不含任何 `if provider == github` 类供应商分支。
func (h *Controller) listAuthProviders(c fiber.Ctx) error {
	if h.providerCatalog == nil {
		return apphttp.OK(c, []authProviderListItem{})
	}
	// 优先走 ExternalAuthService 有效 catalog（含 activation + live artifact + Safe Mode）。
	// locale 仅解析插件声明的展示名；可执行状态仍以 Host activation 为准。
	locale := apphttp.Locale(c)
	if h.externalAuthService != nil {
		entries, err := h.externalAuthService.ListEffectivePublicCatalog(c.Context(), h.providerCatalog, locale)
		if err != nil {
			// Host 状态查询失败：fail closed，不返回部分 catalog。
			return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
		}
		items := make([]authProviderListItem, 0, len(entries))
		for _, e := range entries {
			items = append(items, authProviderListItem{
				ID:                  e.ProviderID,
				Kind:                e.Kind,
				ContractVersion:     e.ContractVersion,
				Priority:            e.Priority,
				Operations:          e.Operations,
				ActivatedOperations: e.ActivatedOperations,
				OwnerExtensionID:    e.OwnerExtensionID,
				Label:               e.Label,
				Icon:                e.Icon,
			})
		}
		return apphttp.OK(c, items)
	}
	// 无 external auth 栈时：不暴露任何需 Host 激活的 auth 入口（默认全 off）。
	// recovery 仍可列出（若 live）。
	items := make([]authProviderListItem, 0)
	for _, provider := range h.providerCatalog.Providers(identityregistry.ProviderKindRecovery) {
		if len(provider.Operations) == 0 || provider.Artifact.Core {
			continue
		}
		if strings.TrimSpace(provider.Artifact.RuntimeInstanceID) == "" {
			continue
		}
		ops := make([]string, 0, len(provider.Operations))
		for _, operation := range provider.Operations {
			ops = append(ops, operation.Name)
		}
		items = append(items, authProviderListItem{
			ID:                  provider.ID,
			Kind:                provider.Kind,
			ContractVersion:     provider.ContractVersion,
			Priority:            provider.Priority,
			Operations:          ops,
			ActivatedOperations: []string{},
			OwnerExtensionID:    provider.Artifact.ExtensionID,
			Label:               identityregistry.ResolveProviderLabel(provider.Provider, locale),
			Icon:                strings.TrimSpace(provider.Icon),
		})
	}
	return apphttp.OK(c, items)
}

type authProviderListItem struct {
	ID                  string   `json:"id"`
	Kind                string   `json:"kind"`
	ContractVersion     string   `json:"contractVersion"`
	Priority            int      `json:"priority"`
	Operations          []string `json:"operations"`
	ActivatedOperations []string `json:"activatedOperations"`
	OwnerExtensionID    string   `json:"ownerExtensionId,omitempty"`
	// Label / Icon 来自插件 Identity 声明（经 Host 按 locale 解析），非 Core 硬编码。
	Label string `json:"label,omitempty"`
	Icon  string `json:"icon,omitempty"`
}

type authProviderStartRequest struct {
	CorrelationID     string `json:"correlationId"`
	DeviceFingerprint string `json:"deviceFingerprint"`
	ClientClass       string `json:"clientClass"`
	RedirectHint      string `json:"redirectHint"`
	AccountHint       string `json:"accountHint"`
}

type authProviderCompleteRequest struct {
	CorrelationID     string `json:"correlationId"`
	CompletionToken   string `json:"completionToken"`
	DeviceFingerprint string `json:"deviceFingerprint"`
	ClientClass       string `json:"clientClass"`
	IdempotencyKey    string `json:"idempotencyKey"`
}

func (h *Controller) authProviderStart(c fiber.Ctx) error {
	providerID := strings.ToLower(strings.TrimSpace(c.Params("providerId")))
	kind := strings.ToLower(strings.TrimSpace(c.Params("operation")))
	operation, err := authProviderStartOperation(kind)
	if err != nil {
		return err
	}
	// M5：start 专用 IP 限流（全局写限流之外）；超限非枚举 429。
	if !h.allowExternalAuthRate(c, "start", identity.ExternalAuthStartMaxPerIP) {
		return fiber.NewError(fiber.StatusTooManyRequests, "rate_limit.exceeded")
	}
	var req authProviderStartRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "invalid request body")
	}
	// recovery.start 走 recovery flow。
	if operation == identity.RecoveryOperationStart {
		if h.recoveryFlow == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
		}
		result, err := h.recoveryFlow.Start(c.Context(), identity.RecoveryProviderStartInput{
			ProviderID: providerID, CorrelationID: req.CorrelationID,
			DeviceFingerprint: req.DeviceFingerprint, ClientClass: req.ClientClass,
			AccountHint: req.AccountHint,
		})
		if err != nil {
			return mapAuthProviderHTTPError(err)
		}
		return apphttp.OK(c, map[string]any{
			"providerId": result.ProviderID, "operation": operation,
			"status": result.Status, "correlationId": result.CorrelationID,
			"continueToken": result.ContinueToken, "redirectUrl": result.RedirectURL,
			"challengeKind": result.ChallengeKind,
		})
	}
	if h.authFlow == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	// T8C：login/registration/link start 在 external-auth 栈不完整时 fail closed。
	// 不得 partial wiring 启动不可用 OAuth（无激活校验、无 callback 事务存储）。
	if err := h.requireExternalAuthStartWiring(); err != nil {
		return err
	}
	// Host 公开激活校验：login/registration/link 必须被 Host 显式激活。
	extOp, opErr := mapAuthStartToExternalOperation(operation)
	if opErr != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	if err := h.externalAuthService.RequireActivated(c.Context(), providerID, extOp); err != nil {
		return mapAuthProviderHTTPError(err)
	}
	browserBindingDigest, err := h.ensureExternalAuthBrowserBinding(c)
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	actorUserID := int64(0)
	if operation == identity.AuthOperationLinkStart {
		actor, actorErr := h.actor(c)
		if actorErr != nil {
			return actorErr
		}
		actorUserID = actor.ID
	}
	// Host 拥有 state、correlation id、PKCE 材料（见 plans/2026-07-27 M1 freeze）。
	// 这些不经过插件返回，而是 Host 生成并存入一次性 callback 事务。
	state, err := identity.GenerateCallbackState()
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	codeVerifier, codeChallenge, err := identity.GeneratePKCE()
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	// 绝对 callback URL 优先来自后台 site.url，未设置时回退 APP_URL；绝不使用请求 Host。
	callbackURL, err := h.absoluteCallbackURL(c.Context(), providerID)
	if err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	// redirectHint 在存入事务或发给插件前必须是安全本地路径。
	safeRedirect := ""
	if identity.ValidateSafeRedirectPath(req.RedirectHint) {
		safeRedirect = strings.TrimSpace(req.RedirectHint)
	}
	result, err := h.authFlow.Start(c.Context(), identity.AuthProviderStartInput{
		ProviderID: providerID, Operation: operation, ActorUserID: actorUserID,
		CorrelationID: req.CorrelationID, DeviceFingerprint: req.DeviceFingerprint,
		ClientClass: req.ClientClass, RedirectHint: safeRedirect,
		// Host 提供给插件用于构造外部 authorize URL 的材料。
		State:         state,
		CodeChallenge: codeChallenge,
		CallbackURL:   callbackURL,
	})
	if err != nil {
		return mapAuthProviderHTTPError(err)
	}
	// 存储一次性 callback 事务：state 绑定 provider/operation/actor/artifact/return/PKCE。
	// 缺 store / catalog / artifact 已在 requireExternalAuthStartWiring 拒绝；此处再解析 live contribution。
	contribution, rErr := h.providerCatalog.ResolveProvider(result.ProviderID)
	if rErr != nil || contribution.Artifact.PackageDigest == "" {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	tx := identity.CallbackTransaction{
		State:                   state,
		ProviderID:              result.ProviderID,
		ProviderContractVersion: contribution.ContractVersion,
		OwnerExtensionID:        contribution.Artifact.ExtensionID,
		OwnerExtensionVersion:   contribution.Artifact.ExtensionVersion,
		OwnerPackageDigest:      contribution.Artifact.PackageDigest,
		Operation:               extOp,
		CorrelationID:           result.CorrelationID,
		ActorUserID:             actorUserID,
		ClientClass:             req.ClientClass,
		DeviceFingerprint:       req.DeviceFingerprint,
		BrowserBindingDigest:    browserBindingDigest,
		RedirectPath:            safeRedirect,
		AbsoluteCallbackURL:     callbackURL,
		CodeChallenge:           codeChallenge,
		CodeVerifier:            codeVerifier,
		CompletionToken:         result.ContinueToken,
		CreatedAt:               time.Now(),
		ExpiresAt:               time.Now().Add(identity.CallbackStateDefaultTTL),
	}
	if err := h.callbackStateStore.Save(c.Context(), tx); err != nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	return apphttp.OK(c, map[string]any{
		"providerId": result.ProviderID, "operation": result.Operation,
		"status": result.Status, "correlationId": result.CorrelationID,
		"continueToken": result.ContinueToken, "redirectUrl": result.RedirectURL,
		"challengeKind": result.ChallengeKind,
	})
}

// requireExternalAuthStartWiring 要求外部 OAuth start 的完整 Host 栈。
// 任一缺失则 fail closed，避免 partial wiring 启动不可用的 OAuth 流。
func (h *Controller) requireExternalAuthStartWiring() error {
	if h == nil ||
		h.externalAuthService == nil ||
		h.callbackStateStore == nil ||
		h.activationStore == nil ||
		h.providerCatalog == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	return nil
}

func (h *Controller) authProviderComplete(c fiber.Ctx) error {
	providerID := strings.ToLower(strings.TrimSpace(c.Params("providerId")))
	kind := strings.ToLower(strings.TrimSpace(c.Params("operation")))
	operation, err := authProviderCompleteOperation(kind)
	if err != nil {
		return err
	}
	var req authProviderCompleteRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "invalid request body")
	}
	// recovery.complete 仍走独立 recovery flow（非外部 OAuth Host callback）。
	if operation == identity.RecoveryOperationComplete {
		if h.recoveryFlow == nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
		}
		result, err := h.recoveryFlow.Complete(c.Context(), identity.RecoveryProviderCompleteInput{
			ProviderID: providerID, CorrelationID: req.CorrelationID,
			CompletionToken: req.CompletionToken, DeviceFingerprint: req.DeviceFingerprint,
			ClientClass: req.ClientClass,
		})
		if err != nil {
			return mapAuthProviderHTTPError(err)
		}
		return apphttp.OK(c, map[string]any{
			"providerId": result.ProviderID, "operation": operation,
			"providerSubjectDigest": result.SubjectDigest, "userHintId": result.UserHintID,
		})
	}
	// T1A：公开 login/registration/link complete 不得绕过 Host callback state、
	// activation、artifact fencing、actor 策略与响应脱敏。业务效应只允许经
	// GET /auth/providers/{id}/callback 完成。本路径保留路由兼容，但 fail closed。
	_ = providerID
	_ = req
	return fiber.NewError(fiber.StatusGone, "auth.provider_callback_required")
}

func authProviderStartOperation(kind string) (string, error) {
	switch kind {
	case "registration":
		return identity.AuthOperationRegistrationStart, nil
	case "login":
		return identity.AuthOperationLoginStart, nil
	case "link":
		return identity.AuthOperationLinkStart, nil
	case "recovery":
		return identity.RecoveryOperationStart, nil
	default:
		return "", fiber.NewError(fiber.StatusNotFound, "auth.provider_operation_not_found")
	}
}

func authProviderCompleteOperation(kind string) (string, error) {
	switch kind {
	case "registration":
		return identity.AuthOperationRegistrationComplete, nil
	case "login":
		return identity.AuthOperationLoginComplete, nil
	case "link":
		return identity.AuthOperationLinkComplete, nil
	case "recovery":
		return identity.RecoveryOperationComplete, nil
	default:
		return "", fiber.NewError(fiber.StatusNotFound, "auth.provider_operation_not_found")
	}
}

func (h *Controller) listProfileSections(c fiber.Ctx) error {
	if h.profileComposer == nil {
		return apphttp.OK(c, []identity.ProfileSection{})
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	sections, err := h.profileComposer.ListSections(c.Context(), actor.ID, actor.ID)
	if err != nil {
		return mapAuthProviderHTTPError(err)
	}
	if sections == nil {
		sections = []identity.ProfileSection{}
	}
	return apphttp.OK(c, sections)
}

type profileSectionUpdateRequest struct {
	ProviderID string         `json:"providerId"`
	Fields     map[string]any `json:"fields"`
}

func (h *Controller) updateProfileSection(c fiber.Ctx) error {
	if h.profileComposer == nil {
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
	actor, err := h.actor(c)
	if err != nil {
		return err
	}
	sectionID := strings.TrimSpace(c.Params("sectionId"))
	var req profileSectionUpdateRequest
	if err := c.Bind().Body(&req); err != nil {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "invalid request body")
	}
	providerID := strings.ToLower(strings.TrimSpace(req.ProviderID))
	if providerID == "" {
		return fiber.NewError(fiber.StatusUnprocessableEntity, "providerId is required")
	}
	section, err := h.profileComposer.UpdateSection(
		c.Context(), providerID, sectionID, actor.ID, actor.ID, req.Fields,
	)
	if err != nil {
		return mapAuthProviderHTTPError(err)
	}
	return apphttp.OK(c, section)
}

func mapAuthProviderHTTPError(err error) error {
	switch {
	case errors.Is(err, identity.ErrAuthProviderNotFound),
		errors.Is(err, identity.ErrProfileProviderNotFound),
		errors.Is(err, identity.ErrRecoveryProviderNotFound):
		return fiber.NewError(fiber.StatusNotFound, "auth.provider_not_found")
	case errors.Is(err, identity.ErrAuthProviderFlowInvalid),
		errors.Is(err, identity.ErrProfileProviderInvalid),
		errors.Is(err, identity.ErrRecoveryProviderInvalid):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.provider_input_invalid")
	case errors.Is(err, identity.ErrAuthProviderFlowUnavailable),
		errors.Is(err, identity.ErrProfileProviderUnavailable),
		errors.Is(err, identity.ErrRecoveryProviderUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	case errors.Is(err, identity.ErrExternalIdentitySubjectConflict):
		return fiber.NewError(fiber.StatusConflict, "auth.external_subject_conflict")
	case errors.Is(err, identity.ErrExternalIdentityLinkStateConflict),
		errors.Is(err, identity.ErrExternalIdentityLinkIdempotencyConflict):
		return fiber.NewError(fiber.StatusConflict, "auth.external_link_conflict")
	default:
		return fiber.NewError(fiber.StatusServiceUnavailable, "auth.provider_unavailable")
	}
}
