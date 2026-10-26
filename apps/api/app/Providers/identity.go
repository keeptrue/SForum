package providers

import (
	"context"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/session"

	identitycontroller "github.com/keeptrue/sforum/apps/api/app/Http/Controllers/Identity"
	apitokens "github.com/keeptrue/sforum/apps/api/app/Models/APITokens"
	identity "github.com/keeptrue/sforum/apps/api/app/Models/Identity"
	notifications "github.com/keeptrue/sforum/apps/api/app/Models/Notifications"
	options "github.com/keeptrue/sforum/apps/api/app/Models/Options"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	appevents "github.com/keeptrue/sforum/apps/api/app/Support/Events"
	humanverify "github.com/keeptrue/sforum/apps/api/app/Support/HumanVerify"
	identityregistry "github.com/keeptrue/sforum/apps/api/app/Support/IdentityRegistry"
)

type IdentityProvider struct {
	controller *identitycontroller.Controller
}

// WithAuthorNameSyncer 注入派生索引同步（站内搜索的作者昵称）。管理员改名与
// 自助改名共用同一条回写路径。
func (p *IdentityProvider) WithAuthorNameSyncer(syncer identity.AuthorNameSyncer) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAuthorNameSyncer(syncer)
	}
	return p
}

func NewIdentityProvider(store identity.Store, sessions *session.Store) *IdentityProvider {
	return NewIdentityProviderWithVerifier(store, sessions, humanverify.NewDisabledService())
}

func NewIdentityProviderWithVerifier(store identity.Store, sessions *session.Store, verifier humanverify.Verifier) *IdentityProvider {
	return NewIdentityProviderWithAuthSessions(store, authsession.NewManager(sessions, authsession.Config{}), verifier)
}

func NewIdentityProviderWithAuthSessions(store identity.Store, sessions *authsession.Manager, verifier humanverify.Verifier) *IdentityProvider {
	return NewIdentityProviderWithEvents(store, sessions, verifier, nil)
}

func NewIdentityProviderWithEvents(store identity.Store, sessions *authsession.Manager, verifier humanverify.Verifier, publisher appevents.Publisher) *IdentityProvider {
	appearanceStore, _ := store.(identity.CurrentUserAppearanceStore)
	localeStore, _ := store.(identity.CurrentUserLocaleStore)
	svc := identity.NewServiceWithEvents(store, publisher)
	return &IdentityProvider{
		controller: identitycontroller.NewControllerWithAuthSessions(svc, sessions, verifier).
			WithAppearancePreferences(identity.NewAppearancePreferenceService(appearanceStore)).
			WithLocalePreferences(identity.NewLocalePreferenceService(localeStore)),
	}
}

// NewIdentityProviderWithPasswordReset 注入密码重置与邮件服务。
type adminMailQueue interface {
	QueueMail(context.Context, notifications.QueueMailInput) (notifications.MailDelivery, error)
}

func NewIdentityProviderWithPasswordReset(store identity.Store, sessions *authsession.Manager, verifier humanverify.Verifier, publisher appevents.Publisher, passwordReset *identity.PasswordResetService, mailQueue adminMailQueue, optionSettings optionsResolver) *IdentityProvider {
	return NewIdentityProviderWithPasswordResetAndLockout(store, sessions, verifier, publisher, passwordReset, mailQueue, optionSettings, nil)
}

// NewIdentityProviderWithPasswordResetAndLockout 额外注入登录失败锁定 store（通常 Redis）。
func NewIdentityProviderWithPasswordResetAndLockout(store identity.Store, sessions *authsession.Manager, verifier humanverify.Verifier, publisher appevents.Publisher, passwordReset *identity.PasswordResetService, mailQueue adminMailQueue, optionSettings optionsResolver, lockout identity.LoginLockoutStore) *IdentityProvider {
	svc := identity.NewServiceWithPolicies(store, publisher, optionSettings, optionSettings)
	if optionSettings != nil {
		svc.WithUsernamePolicy(optionSettings)
		// 登录强化策略（总是/新设备/特权账号）随 Options 实时生效。
		svc.WithLoginVerification(optionSettings)
		if lockout != nil {
			svc.WithLoginLockout(lockout, optionSettings)
		}
	}
	controller := identitycontroller.NewControllerWithPasswordReset(
		svc,
		sessions, verifier, passwordReset, mailQueue, optionSettings,
	)
	appearanceStore, _ := store.(identity.CurrentUserAppearanceStore)
	localeStore, _ := store.(identity.CurrentUserLocaleStore)
	controller.
		WithAppearancePreferences(identity.NewAppearancePreferenceService(appearanceStore)).
		WithLocalePreferences(identity.NewLocalePreferenceService(localeStore))
	if optionService, ok := optionSettings.(*options.Service); ok {
		controller.WithWelcomeMailOptions(options.NewMailSettings(optionService))
	}
	return &IdentityProvider{
		// options 同时作为密码策略与开放注册策略解析器。
		controller: controller,
	}
}

// optionsResolver 暴露密码策略、开放注册开关、用户名策略、登录锁定策略以及密码重置/mail-test 需要的站点名/管理员邮箱。
type optionsResolver interface {
	SiteName(ctx context.Context) (string, error)
	AdminEmail(ctx context.Context) (string, error)
	WebOption(ctx context.Context, name string) (string, error)
	PasswordPolicy(ctx context.Context) (identity.PasswordPolicy, error)
	RegistrationEnabled(ctx context.Context) (bool, error)
	UsernamePolicy(ctx context.Context) (identity.UsernamePolicy, error)
	LoginLockoutPolicy(ctx context.Context) (identity.LoginLockoutPolicy, error)
	LoginVerificationPolicy(ctx context.Context) (identity.LoginVerificationPolicy, error)
}

// WithAPITokens 启用 PAT 管理路由（F3.4）。
func (p *IdentityProvider) WithAPITokens(tokens *apitokens.Service) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAPITokens(tokens)
	}
	return p
}

func (p *IdentityProvider) WithEmailVerification(service *identity.EmailVerificationService) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithEmailVerification(service)
	}
	return p
}

// WithIdentityRegistryStore enables Host-owned permission recommendation
// review without exposing repository mutation methods to extensions.
func (p *IdentityProvider) WithIdentityRegistryStore(store identityregistry.Store) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithIdentityRegistryStore(store)
	}
	return p
}

// WithSessionPolicyEvaluator wires selected session.evaluate before browser
// session issue. Renewal is configured on the AuthSession manager separately.
func (p *IdentityProvider) WithSessionPolicyEvaluator(evaluator *identity.SessionPolicyEvaluator) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithSessionPolicyEvaluator(evaluator)
	}
	return p
}

// WithRiskEvaluator wires composed risk.evaluate before browser session issue.
func (p *IdentityProvider) WithRiskEvaluator(evaluator *identity.RiskEvaluator) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRiskEvaluator(evaluator)
	}
	return p
}

// WithAuthProviderFlow wires Host-owned external auth start/complete consumers.
func (p *IdentityProvider) WithAuthProviderFlow(flow *identity.AuthProviderFlow) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAuthProviderFlow(flow)
	}
	return p
}

// WithProfileProviderComposer wires Host-owned profile section composition.
func (p *IdentityProvider) WithProfileProviderComposer(composer *identity.ProfileProviderComposer) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithProfileProviderComposer(composer)
	}
	return p
}

// WithRecoveryProviderFlow wires Host-owned recovery start/complete consumers.
func (p *IdentityProvider) WithRecoveryProviderFlow(flow *identity.RecoveryProviderFlow) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithRecoveryProviderFlow(flow)
	}
	return p
}

// WithIdentityProviderCatalog wires the live Identity Registry for provider listing.
func (p *IdentityProvider) WithIdentityProviderCatalog(registry *identityregistry.Registry) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithIdentityProviderCatalog(registry)
	}
	return p
}

// WithAuthProviderPackageCatalog wires Host 扩展/包目录 discovery for admin Login Methods.
func (p *IdentityProvider) WithAuthProviderPackageCatalog(catalog identity.AuthProviderPackageCatalog) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithAuthProviderPackageCatalog(catalog)
	}
	return p
}

// WithExternalAuthStack wires the Host-owned external auth orchestration layer
// (callback state, registration tickets, activation catalog, link store, audit).
// See plans/2026-07-27-github-social-login-builtin-plugin.md M1.5.
func (p *IdentityProvider) WithExternalAuthStack(stack identity.ExternalAuthStack) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithExternalAuthService(
			stack.Service,
			stack.CallbackStateStore,
			stack.RegistrationTickets,
			stack.LinkStore,
			stack.ExternalAuthStore,
		)
	}
	return p
}

// WithExternalAuthRateLimiter wires Host-owned start/callback rate limits (M5).
func (p *IdentityProvider) WithExternalAuthRateLimiter(limiter identity.ExternalAuthRateLimiter) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithExternalAuthRateLimiter(limiter)
	}
	return p
}

// WithPublicAppURL injects APP_URL fallback + APP_ENV for absolute OAuth callbacks.
func (p *IdentityProvider) WithPublicAppURL(appURL, appEnv string) *IdentityProvider {
	if p != nil && p.controller != nil {
		p.controller.WithPublicAppURL(appURL, appEnv)
	}
	return p
}

func (p *IdentityProvider) RegisterRoutes(api fiber.Router) {
	p.controller.RegisterRoutes(api)
}
