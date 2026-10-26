package identitycontroller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	apphttp "github.com/keeptrue/sforum/apps/api/app/Http"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	clientip "github.com/keeptrue/sforum/apps/api/app/Support/ClientIP"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
	mail "github.com/keeptrue/sforum/apps/api/app/Support/Mail"
)

var _ optionsResolver = (*options.Service)(nil)

type Controller struct {
	service            *identity.Service
	adminUsers         *identity.AdminUserCreator
	appearance         *identity.AppearancePreferenceService
	localePreferences  *identity.LocalePreferenceService
	authSessions       *authsession.Manager
	verifier           humanverify.Verifier
	passwordReset      *identity.PasswordResetService
	emailVerification  *identity.EmailVerificationService
	mailQueue          adminMailQueue
	options            optionsResolver
	welcomeMailOptions WelcomeMailOptions
	// apiTokens 可选：个人访问令牌（F3.4）。
	apiTokens *apitokens.Service
	auditor   audit.Writer
	// sessionPolicy 在签发浏览器会话前做 selected session.evaluate。
	// 为 nil 时保持历史 Core 默认（测试与未接线启动路径）。
	sessionPolicy *identity.SessionPolicyEvaluator
	// riskPolicy 在密码校验成功后、会话签发前组合全部活跃 risk 提供方。
	// 为 nil 时跳过（无插件 risk 面时的 Core 默认）。
	riskPolicy *identity.RiskEvaluator
	// authFlow / profileComposer / recoveryFlow 是 Host 拥有的 Identity 提供方消费者。
	// 为 nil 时外部 auth/profile/recovery 路径保持不可用。
	authFlow        *identity.AuthProviderFlow
	profileComposer *identity.ProfileProviderComposer
	recoveryFlow    *identity.RecoveryProviderFlow
	// providerCatalog 仅用于红acted 可执行提供方列表。
	providerCatalog *identityregistry.Registry
	// packageCatalog 是 Host 扩展/包目录 discovery（T8B）；live Registry 仍是可执行权威。
	packageCatalog identity.AuthProviderPackageCatalog
	// 外部认证 Host 编排（见 plans/2026-07-27-github-social-login-builtin-plugin.md M1）。
	// 为 nil 时外部登录/注册/link 回调路由返回 unavailable。
	externalAuthService     *identity.ExternalAuthService
	callbackStateStore      identity.CallbackStateStore
	registrationTicketStore identity.RegistrationTicketStore
	externalLinkStore       identity.ExternalIdentityLinkStore
	externalAuthStore       *identity.PostgresExternalAuthStore
	activationStore         identity.ProviderActivationStore
	// externalAuthRateLimiter 保护 start/callback（M5）；nil 时跳过专用限流。
	externalAuthRateLimiter identity.ExternalAuthRateLimiter
	// appURL 是环境 APP_URL fallback；OAuth callback 优先使用运行时 site.url。
	// 绝不使用请求 Host。appEnv 用于生产 HTTPS 强制。
	appURL string
	appEnv string
	// authorNames 可选：管理员改名后回写派生索引（站内搜索的作者昵称）。
	authorNames identity.AuthorNameSyncer
}

// WithAuthorNameSyncer 注入改名后的派生索引同步。它是可选依赖：同步失败只记
// 日志，不回滚已经提交的账户更新。
func (h *Controller) WithAuthorNameSyncer(syncer identity.AuthorNameSyncer) *Controller {
	if h != nil {
		h.authorNames = syncer
	}
	return h
}

// optionsResolver 只暴露密码策略、mail-test 需要的站点名/管理员邮箱，避免全量依赖 options.Service。
type optionsResolver interface {
	SiteName(ctx context.Context) (string, error)
	AdminEmail(ctx context.Context) (string, error)
	WebOption(ctx context.Context, name string) (string, error)
	PasswordPolicy(ctx context.Context) (identity.PasswordPolicy, error)
}

func NewController(service *identity.Service, sessions *session.Store) *Controller {
	return NewControllerWithVerifier(service, sessions, humanverify.NewDisabledService())
}

func NewControllerWithVerifier(service *identity.Service, sessions *session.Store, verifier humanverify.Verifier) *Controller {
	return NewControllerWithAuthSessions(service, authsession.NewManager(sessions, authsession.Config{}), verifier)
}

func NewControllerWithAuthSessions(service *identity.Service, sessions *authsession.Manager, verifier humanverify.Verifier) *Controller {
	return NewControllerWithPasswordReset(service, sessions, verifier, nil, nil, nil)
}

// NewControllerWithPasswordReset 注入密码重置与邮件服务。
type adminMailQueue interface {
	QueueMail(context.Context, notifications.QueueMailInput) (notifications.MailDelivery, error)
}

type WelcomeMailOptions interface {
	WelcomeMailEnabled(context.Context) (bool, error)
	MailBrand(context.Context) (mail.Brand, error)
}

func NewControllerWithPasswordReset(service *identity.Service, sessions *authsession.Manager, verifier humanverify.Verifier, passwordReset *identity.PasswordResetService, mailQueue adminMailQueue, options optionsResolver) *Controller {
	if verifier == nil {
		verifier = humanverify.NewDisabledService()
	}
	return &Controller{
		service:       service,
		adminUsers:    identity.NewAdminUserCreator(service),
		authSessions:  sessions,
		verifier:      verifier,
		passwordReset: passwordReset,
		mailQueue:     mailQueue,
		options:       options,
	}
}

func (h *Controller) WithWelcomeMailOptions(settings WelcomeMailOptions) *Controller {
	if h != nil {
		h.welcomeMailOptions = settings
	}
	return h
}

func (h *Controller) WithEmailVerification(service *identity.EmailVerificationService) *Controller {
	if h != nil {
		h.emailVerification = service
	}
	return h
}

func (h *Controller) WithAppearancePreferences(service *identity.AppearancePreferenceService) *Controller {
	if h != nil {
		h.appearance = service
	}
	return h
}

func (h *Controller) WithLocalePreferences(service *identity.LocalePreferenceService) *Controller {
	if h != nil {
		h.localePreferences = service
	}
	return h
}

// queueWelcomeMail is deliberately best-effort: an already-created account and
// successful session must not be rolled back by a mail queue outage.
func (h *Controller) queueWelcomeMail(ctx context.Context, recipient, browserLocale string, current identity.CurrentUser) {
	if h.mailQueue == nil || h.options == nil {
		return
	}
	if h.welcomeMailOptions == nil {
		return
	}
	enabled, err := h.welcomeMailOptions.WelcomeMailEnabled(ctx)
	if err != nil || !enabled {
		return
	}
	locale := strings.TrimSpace(browserLocale)
	if locale == "" {
		locale = strings.TrimSpace(current.Locale)
	}
	if locale == "" {
		locale, _ = h.resolveUserLocale(ctx, "")
	}
	siteName := "SForum"
	if value, err := h.options.SiteName(ctx); err == nil && strings.TrimSpace(value) != "" {
		siteName = strings.TrimSpace(value)
	}
	siteURL, _ := h.options.WebOption(ctx, "site.url")
	brand, brandErr := h.welcomeMailOptions.MailBrand(ctx)
	if brandErr != nil {
		brand = mail.DefaultBrand(siteName, siteURL)
	}
	data := brand.TemplateData()
	data["locale"] = locale
	data["username"] = current.DisplayName
	data["siteName"] = siteName
	data["siteUrl"] = strings.TrimSpace(siteURL)
	encoded, err := json.Marshal(data)
	if err != nil {
		return
	}
	_, _ = h.mailQueue.QueueMail(ctx, notifications.QueueMailInput{
		Recipient: strings.TrimSpace(recipient), TemplateKey: "identity.welcome", TemplateData: encoded,
		IdempotencyKey: fmt.Sprintf("identity_welcome:%d", current.ID),
	})
}

// WithAPITokens 注入 PAT 服务（账号安全页创建/轮换/撤销）。
func (h *Controller) WithAPITokens(tokens *apitokens.Service) *Controller {
	if h != nil {
		h.apiTokens = tokens
	}
	return h
}

func (h *Controller) WithAuditor(w audit.Writer) *Controller {
	if h != nil {
		h.auditor = w
	}
	return h
}

// WithSessionPolicyEvaluator injects Host-owned selected session policy
// evaluation before browser session issue. Renewal is wired on AuthSession.
func (h *Controller) WithSessionPolicyEvaluator(evaluator *identity.SessionPolicyEvaluator) *Controller {
	if h != nil {
		h.sessionPolicy = evaluator
	}
	return h
}

// WithRiskEvaluator injects Host-owned risk composition before session issue.
func (h *Controller) WithRiskEvaluator(evaluator *identity.RiskEvaluator) *Controller {
	if h != nil {
		h.riskPolicy = evaluator
	}
	return h
}

// WithAuthProviderFlow injects Host-owned external auth start/complete consumers.
func (h *Controller) WithAuthProviderFlow(flow *identity.AuthProviderFlow) *Controller {
	if h != nil {
		h.authFlow = flow
	}
	return h
}

// WithProfileProviderComposer injects Host-owned profile section composition.
func (h *Controller) WithProfileProviderComposer(composer *identity.ProfileProviderComposer) *Controller {
	if h != nil {
		h.profileComposer = composer
	}
	return h
}

// WithRecoveryProviderFlow injects Host-owned recovery start/complete consumers.
func (h *Controller) WithRecoveryProviderFlow(flow *identity.RecoveryProviderFlow) *Controller {
	if h != nil {
		h.recoveryFlow = flow
	}
	return h
}

// WithIdentityProviderCatalog injects the live Identity Registry for provider listing.
func (h *Controller) WithIdentityProviderCatalog(registry *identityregistry.Registry) *Controller {
	if h != nil {
		h.providerCatalog = registry
	}
	return h
}

// WithAuthProviderPackageCatalog injects Host 扩展/包目录 discovery（T8B）。
// live Registry 保持可执行权威；本目录仅用于 admin 展示 discovered 状态。
func (h *Controller) WithAuthProviderPackageCatalog(catalog identity.AuthProviderPackageCatalog) *Controller {
	if h != nil {
		h.packageCatalog = catalog
	}
	return h
}

// WithExternalAuthService 注入外部认证 Host 编排层与回调/票据/链接存储。
// 见 plans/2026-07-27-github-social-login-builtin-plugin.md M1。
func (h *Controller) WithExternalAuthService(
	svc *identity.ExternalAuthService,
	callbackStore identity.CallbackStateStore,
	ticketStore identity.RegistrationTicketStore,
	linkStore identity.ExternalIdentityLinkStore,
	externalAuthStore *identity.PostgresExternalAuthStore,
) *Controller {
	if h != nil {
		h.externalAuthService = svc
		h.callbackStateStore = callbackStore
		h.registrationTicketStore = ticketStore
		h.externalLinkStore = linkStore
		h.externalAuthStore = externalAuthStore
		if svc != nil {
			// 复用 service 依赖里的 activationStore，避免再传一个参数。
			h.activationStore = svc.ActivationStore()
			// T1D：外部注册复用权威 identity.Service 字段/策略校验，不维护更弱副本。
			if h.service != nil {
				svc.WithRegistrationValidator(h.service)
			}
		}
	}
	return h
}

// WithPublicAppURL 注入环境 APP_URL fallback 与运行环境。
// 外部 OAuth 绝对 callback URL 优先使用运行时 site.url，且不信任请求 Host。
func (h *Controller) WithPublicAppURL(appURL, appEnv string) *Controller {
	if h != nil {
		h.appURL = strings.TrimSpace(appURL)
		h.appEnv = strings.TrimSpace(appEnv)
	}
	return h
}

// WithExternalAuthRateLimiter 注入外部认证 start/callback 专用限流（M5）。
func (h *Controller) WithExternalAuthRateLimiter(limiter identity.ExternalAuthRateLimiter) *Controller {
	if h != nil {
		h.externalAuthRateLimiter = limiter
	}
	return h
}

// allowExternalAuthRate 按 IP + scope 检查限流；未配置 limiter 时放行。
// 超限返回 false；调用方映射为 rate_limit.exceeded / 安全 redirect。
func (h *Controller) allowExternalAuthRate(c fiber.Ctx, scope string, max int) bool {
	if h == nil || h.externalAuthRateLimiter == nil || max <= 0 {
		return true
	}
	ip := clientip.FromCtx(c)
	key := identity.ExternalAuthRateKey(scope, ip)
	ok, err := h.externalAuthRateLimiter.Allow(c.Context(), key, max, identity.ExternalAuthRateWindow)
	if err != nil {
		// 与 Redis fail-open 一致：错误时不阻断。
		return true
	}
	return ok
}

// absoluteCallbackURL 优先从后台 site.url 生成绝对 callback；未设置时回退 APP_URL。
// 配置读取失败时 fail closed，生产环境始终强制 HTTPS。
func (h *Controller) absoluteCallbackURL(ctx context.Context, providerID string) (string, error) {
	baseURL := strings.TrimSpace(h.appURL)
	if h.options != nil {
		siteURL, err := h.options.WebOption(ctx, options.NameSiteURL)
		if err != nil {
			return "", err
		}
		if siteURL = strings.TrimSpace(siteURL); siteURL != "" {
			baseURL = siteURL
		}
	}
	requireHTTPS := strings.EqualFold(h.appEnv, "production")
	return identity.AbsoluteExternalAuthCallbackURL(baseURL, providerID, requireHTTPS)
}

type registerRequest struct {
	Username          string                   `json:"username"`
	Email             string                   `json:"email"`
	Password          string                   `json:"password"`
	DisplayName       string                   `json:"displayName"`
	Locale            string                   `json:"locale"`
	HumanVerification humanVerificationRequest `json:"humanVerification"`
	// StepUpEvidence is Host-minted one-use session policy evidence.
	StepUpEvidence string `json:"stepUpEvidence"`
}

type loginRequest struct {
	Login             string                   `json:"login"`
	Password          string                   `json:"password"`
	HumanVerification humanVerificationRequest `json:"humanVerification"`
	// StepUpEvidence is Host-minted one-use session policy evidence.
	StepUpEvidence string `json:"stepUpEvidence"`
	// IssueAPIToken 可选：原生/机器客户端在密码登录成功后顺带签发一枚 PAT，
	// 免去「cookie 会话 + CSRF 二次换取令牌」。载荷与 POST /auth/tokens 完全同形。
	IssueAPIToken *createAPITokenRequest `json:"issueApiToken"`
}

type humanVerificationRequest struct {
	Provider string `json:"provider"`
	Token    string `json:"token"`
}

type emailVerificationRequest struct {
	HumanVerification humanVerificationRequest `json:"humanVerification"`
}

type roleRequest struct {
	Key         string `json:"key"`
	Alias       string `json:"alias"`
	Description string `json:"description"`
}

type replaceRolePermissionsRequest struct {
	Permissions []string `json:"permissions"`
}

type replaceUserRolesRequest struct {
	RoleKeys []string `json:"roleKeys"`
}

type replaceUserPermissionOverridesRequest struct {
	Allow []string `json:"allow"`
	Deny  []string `json:"deny"`
}

// updateUserRequest 管理员 PATCH 用户；字段可选，未传则不改。
type updateUserRequest struct {
	Username    *string `json:"username"`
	Email       *string `json:"email"`
	DisplayName *string `json:"displayName"`
	Locale      *string `json:"locale"`
	Status      *string `json:"status"`
	// Kind 允许管理员把一个账号标记为机器人，或恢复为人类。
	Kind       *string `json:"kind"`
	Bio        *string `json:"bio"`
	Signature  *string `json:"signature"`
	Location   *string `json:"location"`
	WebsiteURL *string `json:"websiteUrl"`
}

// createUserRequest 管理员创建用户；password 与 generatePassword 二选一。
type createUserRequest struct {
	Username         string   `json:"username"`
	Email            string   `json:"email"`
	DisplayName      string   `json:"displayName"`
	Password         string   `json:"password"`
	GeneratePassword bool     `json:"generatePassword"`
	RoleKeys         []string `json:"roleKeys"`
	EmailVerified    bool     `json:"emailVerified"`
}

type setUserEmailVerificationRequest struct {
	Verified *bool `json:"verified"`
}

func queryInt(c fiber.Ctx, name string) int {
	value, err := strconv.Atoi(c.Query(name))
	if err != nil {
		return 0
	}
	return value
}

func paramInt64(c fiber.Ctx, name string) (int64, error) {
	return strconv.ParseInt(c.Params(name), 10, 64)
}

func mapIdentityError(err error) error {
	var adminFields *identity.AdminUserFieldsError
	var adminConflict *identity.AdminUserConflictError
	var registerErr *identity.RegisterInvalidError
	var rejected *appevents.RejectedError
	var resendLimited *identity.MailResendRateLimitError
	switch {
	// 管理端用户写入：字段级错误必须带 fields 出去，否则前端只能显示一句泛化提示。
	// 这条分支排在 RegisterInvalidError 之前，注册链路的 auth.register_invalid 不受影响。
	case errors.As(err, &adminFields):
		// data.reason 是契约必填项，零值不能漏到响应里。
		reason := adminFields.Reason
		if reason == "" {
			reason = identity.CodeUserInvalidUpdate
		}
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, reason, adminFields.Fields)
	case errors.As(err, &adminConflict):
		return apphttp.NewErrorWithFields(fiber.StatusConflict, identity.CodeUsernameOrEmailTaken, adminConflict.Fields)
	case errors.As(err, &registerErr):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, identity.CodeRegisterInvalid, registerErr.Fields)
	// 插件 user.before_register 等同步拒绝：422 + 稳定 reason，不暴露堆栈。
	case errors.As(err, &rejected):
		return fiber.NewError(fiber.StatusUnprocessableEntity, rejected.Reason)
	case errors.As(err, &resendLimited):
		reason := "auth.password_reset_rate_limited"
		if errors.Is(err, identity.ErrEmailVerificationRateLimited) {
			reason = "auth.email_verification_rate_limited"
		}
		return apphttp.NewErrorWithRetryAt(fiber.StatusTooManyRequests, reason, resendLimited.RetryAt)
	case errors.Is(err, identity.ErrInvalidCredentials):
		return fiber.NewError(fiber.StatusUnauthorized, "auth.invalid_credentials")
	case errors.Is(err, identity.ErrEmailVerificationTokenNotFound):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.email_verification_invalid")
	case errors.Is(err, identity.ErrEmailVerificationRateLimited):
		return fiber.NewError(fiber.StatusTooManyRequests, "auth.email_verification_rate_limited")
	case errors.Is(err, identity.ErrEmailVerificationRequired):
		return fiber.NewError(fiber.StatusForbidden, "auth.email_verification_required")
	case errors.Is(err, identity.ErrLoginLocked):
		return fiber.NewError(fiber.StatusTooManyRequests, identity.CodeLoginLocked)
	case errors.Is(err, identity.ErrLoginVerificationRequired):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "human_verification.required")
	case errors.Is(err, identity.ErrSessionPolicyEvaluationStepUp):
		// Session policy step-up reuses the login-risk human verification surface
		// and returns Host-minted one-use evidence when available.
		var stepUp *identity.SessionPolicyStepUpRequiredError
		if errors.As(err, &stepUp) && stepUp.Token != "" {
			return apphttp.NewErrorWithFields(
				fiber.StatusUnprocessableEntity,
				"human_verification.required",
				map[string][]string{"stepUpEvidence": {stepUp.Token}},
			)
		}
		return fiber.NewError(fiber.StatusUnprocessableEntity, "human_verification.required")
	case errors.Is(err, identity.ErrSessionPolicyStepUpInvalid),
		errors.Is(err, identity.ErrSessionPolicyStepUpExpired),
		errors.Is(err, identity.ErrSessionPolicyStepUpReplayed),
		errors.Is(err, identity.ErrSessionPolicyStepUpStale):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.session_policy_step_up_invalid")
	case errors.Is(err, identity.ErrSessionPolicyEvaluationDenied),
		errors.Is(err, identity.ErrRiskEvaluationDenied):
		return fiber.NewError(fiber.StatusForbidden, identity.CodeSessionPolicyDenied)
	case errors.Is(err, identity.ErrRiskEvaluationStepUp):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "human_verification.required")
	case errors.Is(err, identity.ErrRiskEvaluationUnavailable),
		errors.Is(err, identity.ErrRiskEvaluationInvalid):
		return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
	case errors.Is(err, identity.ErrSessionPolicyEvaluationUnavailable),
		errors.Is(err, identity.ErrSessionPolicyEvaluationStale),
		errors.Is(err, identity.ErrSessionPolicyEvaluationInvalid),
		errors.Is(err, identity.ErrIdentitySessionPolicyDeclarationStale),
		errors.Is(err, identity.ErrIdentitySessionPolicyStoreUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, identity.CodeSessionUnavailable)
	case errors.Is(err, identity.ErrRegistrationDisabled):
		// 关闭开放注册：403 + 稳定错误码，便于前端展示“注册已关闭”。
		return fiber.NewError(fiber.StatusForbidden, identity.CodeRegisterDisabled)
	case errors.Is(err, identity.ErrPermissionDenied):
		return fiber.NewError(fiber.StatusForbidden, "permission.denied")
	case errors.Is(err, identity.ErrInvalidPermission):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "permission.invalid")
	case errors.Is(err, identity.ErrInvalidRole):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "role.invalid")
	case errors.Is(err, identity.ErrInvalidRoleInput):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "role.invalid_input")
	case errors.Is(err, identity.ErrPermissionOverrideConflict):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "permission.override_conflict")
	case errors.Is(err, identity.ErrSystemRoleLocked):
		return fiber.NewError(fiber.StatusConflict, "role.system_role_locked")
	case errors.Is(err, identity.ErrDefaultRoleLocked):
		return fiber.NewError(fiber.StatusConflict, "role.default_role_locked")
	case errors.Is(err, identity.ErrInitialSuperAdminLocked):
		return fiber.NewError(fiber.StatusConflict, "user.initial_super_admin_locked")
	case errors.Is(err, identity.ErrSuperAdminOverridesLocked):
		return fiber.NewError(fiber.StatusConflict, "user.super_admin_permissions_locked")
	case errors.Is(err, identity.ErrSelfRoleChange):
		return fiber.NewError(fiber.StatusForbidden, "user.cannot_change_self_roles")
	case errors.Is(err, identity.ErrSuperAdminGrantRestricted):
		return fiber.NewError(fiber.StatusForbidden, "user.super_admin_grant_restricted")
	case errors.Is(err, identity.ErrPasswordDoesNotMeetPolicy):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.password_policy")
	case errors.Is(err, identity.ErrPasswordResetTokenNotFound):
		// 无效/过期/已消费令牌：稳定 422，避免落到 500 internal_error。
		return fiber.NewError(fiber.StatusUnprocessableEntity, "auth.password_reset_invalid")
	case errors.Is(err, identity.ErrPasswordResetRateLimited):
		return fiber.NewError(fiber.StatusTooManyRequests, "auth.password_reset_rate_limited")
	case errors.Is(err, identity.ErrSessionNotFound):
		return fiber.NewError(fiber.StatusNotFound, "auth.session_not_found")
	case errors.Is(err, identity.ErrSelfSessionRevoke):
		return fiber.NewError(fiber.StatusBadRequest, "auth.cannot_revoke_own_sessions")
	case errors.Is(err, identity.ErrSuperAdminSessionLocked):
		return fiber.NewError(fiber.StatusForbidden, "auth.super_admin_session_locked")
	case errors.Is(err, identity.ErrUserNotFound):
		return fiber.NewError(fiber.StatusNotFound, "user.not_found")
	case errors.Is(err, identity.ErrInvalidUserUpdate):
		return fiber.NewError(fiber.StatusUnprocessableEntity, identity.CodeUserInvalidUpdate)
	case errors.Is(err, identity.ErrUserLocaleUpdateUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "identity.locale_unavailable")
	case errors.Is(err, identity.ErrInvalidAppearancePreference):
		return fiber.NewError(fiber.StatusUnprocessableEntity, "appearance.invalid")
	case errors.Is(err, identity.ErrAppearanceUpdateUnavailable):
		return fiber.NewError(fiber.StatusServiceUnavailable, "appearance.unavailable")
	case errors.Is(err, identity.ErrSelfStatusChange):
		return fiber.NewError(fiber.StatusForbidden, "user.cannot_change_own_status")
	case errors.Is(err, identity.ErrUsernameOrEmailNotUnique):
		return fiber.NewError(fiber.StatusConflict, identity.CodeUsernameOrEmailTaken)
	default:
		return err
	}
}

func mapHumanVerificationError(err error) error {
	switch {
	case errors.Is(err, humanverify.ErrRateLimited):
		return apphttp.NewErrorWithFields(fiber.StatusTooManyRequests, humanverify.CodeRateLimited, mapHumanVerificationField(humanverify.CodeRateLimited))
	case errors.Is(err, humanverify.ErrRequired):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeRequired, mapHumanVerificationField(humanverify.CodeRequired))
	case errors.Is(err, humanverify.ErrExpired):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeExpired, mapHumanVerificationField(humanverify.CodeExpired))
	case errors.Is(err, humanverify.ErrReplayed):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeReplayed, mapHumanVerificationField(humanverify.CodeReplayed))
	case errors.Is(err, humanverify.ErrInvalid):
		return apphttp.NewErrorWithFields(fiber.StatusUnprocessableEntity, humanverify.CodeInvalid, mapHumanVerificationField(humanverify.CodeInvalid))
	default:
		return err
	}
}

func mapHumanVerificationField(message string) map[string][]string {
	return map[string][]string{
		identity.FieldHumanVerification: {message},
	}
}

func parseHumanVerificationPurpose(value string) (humanverify.Purpose, bool) {
	switch humanverify.Purpose(value) {
	case humanverify.PurposeRegister, humanverify.PurposePasswordReset, humanverify.PurposeEmailVerification, humanverify.PurposeLoginRisk, humanverify.PurposePostRisk:
		return humanverify.Purpose(value), true
	default:
		return "", false
	}
}
