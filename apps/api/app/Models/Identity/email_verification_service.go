package identity

import (
	"context"
	"fmt"
	"strings"
	"time"

	mail "github.com/keeptrue/sforum/apps/api/app/Support/Mail"
)

type EmailVerificationPolicy struct {
	Required     bool
	BlockContent bool
}

type EmailVerificationPolicyResolver interface {
	EmailVerificationPolicy(context.Context) (EmailVerificationPolicy, error)
}

type EmailVerificationConfig struct {
	TokenTTL          time.Duration
	VerifyPathBase    string
	RequestMaxPerUser int
	RequestMaxPerIP   int
	RequestCooldown   time.Duration
	RequestWindow     time.Duration
}

type EmailVerificationMail struct {
	Recipient, IdempotencyKey   string
	Locale, Username, VerifyURL string
	ExpiresAt                   time.Time
	Brand                       mail.Brand
}

type EmailVerificationQueue interface {
	QueueEmailVerification(context.Context, CreateEmailVerificationTokenInput, EmailVerificationMail) error
}

type EmailVerificationRateLimiter = MailResendRateLimiter

type EmailVerificationService struct {
	store          EmailVerificationStore
	queue          EmailVerificationQueue
	policy         EmailVerificationPolicyResolver
	config         EmailVerificationConfig
	rateLimiter    EmailVerificationRateLimiter
	resendPolicies MailResendPolicyResolver
	localeResolver PasswordResetLocaleResolver
	brandResolver  PasswordResetBrandResolver
}

func NewEmailVerificationService(store EmailVerificationStore, queue EmailVerificationQueue, policy EmailVerificationPolicyResolver, config EmailVerificationConfig) *EmailVerificationService {
	if config.TokenTTL <= 0 {
		config.TokenTTL = 24 * time.Hour
	}
	if strings.TrimSpace(config.VerifyPathBase) == "" {
		config.VerifyPathBase = "/api/v1/auth/email-verification/confirm?token="
	}
	if config.RequestMaxPerUser <= 0 {
		config.RequestMaxPerUser = 3
	}
	if config.RequestMaxPerIP <= 0 {
		config.RequestMaxPerIP = 10
	}
	if config.RequestCooldown <= 0 {
		config.RequestCooldown = RecommendedMailResendCooldown
	}
	if config.RequestWindow <= 0 {
		config.RequestWindow = time.Hour
	}
	return &EmailVerificationService{store: store, queue: queue, policy: policy, config: config}
}

func (s *EmailVerificationService) WithRateLimiter(limiter EmailVerificationRateLimiter) *EmailVerificationService {
	s.rateLimiter = limiter
	return s
}

func (s *EmailVerificationService) WithMailResendPolicyResolver(resolver MailResendPolicyResolver) *EmailVerificationService {
	s.resendPolicies = resolver
	return s
}

func (s *EmailVerificationService) WithMailResolvers(locale PasswordResetLocaleResolver, brand PasswordResetBrandResolver) *EmailVerificationService {
	s.localeResolver = locale
	s.brandResolver = brand
	return s
}

func (s *EmailVerificationService) Request(ctx context.Context, userID int64, ip, browserLocale string) (bool, error) {
	result, err := s.RequestWithResult(ctx, userID, ip, browserLocale)
	return result.Sent, err
}

func (s *EmailVerificationService) RequestWithResult(ctx context.Context, userID int64, ip, browserLocale string) (MailResendResult, error) {
	if s == nil || s.store == nil || userID <= 0 {
		return MailResendResult{}, ErrUserNotFound
	}
	policy, err := s.resolvePolicy(ctx)
	if err != nil || !policy.Required {
		return MailResendResult{}, err
	}
	target, err := s.store.GetEmailVerificationTarget(ctx, userID)
	if err != nil {
		return MailResendResult{}, err
	}
	if target.Status != UserStatusActive || target.EmailVerified {
		return MailResendResult{}, nil
	}
	retryAt, err := s.enforceRateLimit(ctx, userID, ip)
	if err != nil {
		return MailResendResult{}, err
	}
	result := MailResendResult{Sent: true, RetryAt: retryAt}
	rawToken, err := generateResetToken()
	if err != nil {
		return result, err
	}
	tokenHash := hashResetToken(rawToken)
	expiresAt := time.Now().UTC().Add(s.config.TokenTTL)
	input := CreateEmailVerificationTokenInput{
		UserID: userID, Email: target.Email, TokenHash: tokenHash,
		ExpiresAt: expiresAt, RequestIPHash: hashIP(ip),
	}
	if s.queue == nil {
		_, err = s.store.CreateEmailVerificationToken(ctx, input)
		return result, err
	}
	brand := mail.ResolveBrand(ctx, s.brandResolver)
	message := EmailVerificationMail{
		Recipient: target.Email, IdempotencyKey: "email_verification:" + tokenHash,
		Locale: s.resolveLocale(ctx, browserLocale, target.Locale), Username: target.Username,
		VerifyURL: s.buildVerifyURL(brand.SiteURL, rawToken), ExpiresAt: expiresAt, Brand: brand,
	}
	if err := s.queue.QueueEmailVerification(ctx, input, message); err != nil {
		return result, err
	}
	return result, nil
}

func (s *EmailVerificationService) Confirm(ctx context.Context, rawToken string) (int64, error) {
	if s == nil || s.store == nil || strings.TrimSpace(rawToken) == "" {
		return 0, ErrEmailVerificationTokenNotFound
	}
	return s.store.ConfirmEmailVerification(ctx, hashResetToken(strings.TrimSpace(rawToken)))
}

func (s *EmailVerificationService) SetByAdmin(ctx context.Context, actor Actor, targetUserID int64, verified bool) (AdminUserDetail, error) {
	if s == nil || s.store == nil || targetUserID <= 0 {
		return AdminUserDetail{}, ErrUserNotFound
	}
	if !actor.Can(PermissionUserManage) {
		return AdminUserDetail{}, ErrPermissionDenied
	}
	target, err := s.store.GetAdminUser(ctx, targetUserID)
	if err != nil {
		return AdminUserDetail{}, err
	}
	if containsString(target.RoleKeys, RoleSuperAdmin) && !actor.IsSuperAdmin() {
		return AdminUserDetail{}, ErrSuperAdminSessionLocked
	}
	return s.store.SetAdminUserEmailVerified(ctx, actor.ID, targetUserID, verified)
}

func (s *EmailVerificationService) RequireVerifiedForContent(ctx context.Context, userID int64) error {
	if s == nil || s.store == nil {
		return nil
	}
	policy, err := s.resolvePolicy(ctx)
	if err != nil {
		return err
	}
	if !policy.Required || !policy.BlockContent {
		return nil
	}
	verified, err := s.store.IsEmailVerified(ctx, userID)
	if err != nil {
		return err
	}
	if !verified {
		return ErrEmailVerificationRequired
	}
	return nil
}

func (s *EmailVerificationService) resolvePolicy(ctx context.Context) (EmailVerificationPolicy, error) {
	if s.policy == nil {
		return EmailVerificationPolicy{}, nil
	}
	return s.policy.EmailVerificationPolicy(ctx)
}

func (s *EmailVerificationService) enforceRateLimit(ctx context.Context, userID int64, ip string) (time.Time, error) {
	policy, err := s.mailResendPolicy(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return enforceMailResendPolicy(ctx, s.rateLimiter, "email_verification", fmt.Sprintf("user:%d", userID), ip, policy, ErrEmailVerificationRateLimited)
}

func (s *EmailVerificationService) mailResendPolicy(ctx context.Context) (MailResendPolicy, error) {
	if s.resendPolicies != nil {
		return s.resendPolicies.MailResendPolicy(ctx)
	}
	return MailResendPolicy{
		Cooldown: s.config.RequestCooldown, Window: s.config.RequestWindow,
		MaxPerTarget: s.config.RequestMaxPerUser, MaxPerIP: s.config.RequestMaxPerIP,
	}, nil
}

func (s *EmailVerificationService) resolveLocale(ctx context.Context, browserLocale, userLocale string) string {
	if locale := strings.TrimSpace(browserLocale); locale != "" {
		return locale
	}
	if locale := strings.TrimSpace(userLocale); locale != "" {
		return locale
	}
	if s.localeResolver != nil {
		if locale, err := s.localeResolver.DefaultMailLocale(ctx); err == nil && strings.TrimSpace(locale) != "" {
			return locale
		}
	}
	return "zh-CN"
}

func (s *EmailVerificationService) buildVerifyURL(siteURL, rawToken string) string {
	base := strings.TrimRight(strings.TrimSpace(siteURL), "/")
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	return base + s.config.VerifyPathBase + rawToken
}
