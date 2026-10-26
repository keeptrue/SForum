package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	mail "github.com/keeptrue/sforum/apps/api/app/Support/Mail"
)

// PasswordResetConfig 是密码重置流程的可选依赖。
type PasswordResetConfig struct {
	// TokenTTL 令牌有效期，默认 30 分钟。
	TokenTTL time.Duration
	// SiteName / SiteURL 用于邮件正文。
	SiteName string
	SiteURL  string
	// ResetPathBase 是前端重置密码页路径前缀，默认 /reset-password?token=。
	ResetPathBase string
	// RequestMaxPerEmail / RequestMaxPerIP 请求限流（窗口内最大次数），默认 3 / 10。
	RequestMaxPerEmail int
	RequestMaxPerIP    int
	// RequestCooldown 是同一邮箱两次成功请求之间的最短间隔，默认 30 秒。
	RequestCooldown time.Duration
	// RequestWindow 限流窗口，默认 1 小时。
	RequestWindow time.Duration
}

// PasswordResetRateLimiter 密码重置请求限流（通常 Redis）。
type PasswordResetRateLimiter = MailResendRateLimiter

// PasswordResetService 协调密码重置：生成令牌、投递邮件、校验与消费令牌、更新密码。
type PasswordResetService struct {
	store            Store
	mailer           PasswordResetQueue
	config           PasswordResetConfig
	passwordPolicies PasswordPolicyResolver
	rateLimiter      PasswordResetRateLimiter
	resendPolicies   MailResendPolicyResolver
	localeResolver   PasswordResetLocaleResolver
	brandResolver    PasswordResetBrandResolver
}

type PasswordResetMail struct {
	Recipient, IdempotencyKey  string
	Locale, Username, ResetURL string
	ExpiresAt                  time.Time
	SiteName                   string
	Brand                      mail.Brand
}
type PasswordResetQueue interface {
	QueuePasswordReset(context.Context, CreatePasswordResetTokenInput, PasswordResetMail) error
}

// PasswordResetLocaleResolver keeps the site-default fallback live without
// coupling the identity workflow to the options implementation.
type PasswordResetLocaleResolver interface {
	DefaultMailLocale(context.Context) (string, error)
}

type PasswordResetBrandResolver interface {
	MailBrand(context.Context) (mail.Brand, error)
}

func NewPasswordResetService(store Store, mailer PasswordResetQueue, config PasswordResetConfig) *PasswordResetService {
	return NewPasswordResetServiceWithPasswordPolicy(store, mailer, config, nil)
}

func NewPasswordResetServiceWithPasswordPolicy(store Store, mailer PasswordResetQueue, config PasswordResetConfig, resolver PasswordPolicyResolver) *PasswordResetService {
	if config.TokenTTL <= 0 {
		config.TokenTTL = 30 * time.Minute
	}
	if config.ResetPathBase == "" {
		config.ResetPathBase = "/reset-password?token="
	}
	if config.RequestMaxPerEmail <= 0 {
		config.RequestMaxPerEmail = 3
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
	if resolver == nil {
		resolver = staticRecommendedPasswordPolicy{}
	}
	return &PasswordResetService{store: store, mailer: mailer, config: config, passwordPolicies: resolver}
}

// WithRateLimiter 注入 Redis 等限流后端；未注入时跳过限流（测试/无 Redis 场景）。
func (s *PasswordResetService) WithRateLimiter(limiter PasswordResetRateLimiter) *PasswordResetService {
	if s != nil {
		s.rateLimiter = limiter
	}
	return s
}

func (s *PasswordResetService) WithMailResendPolicyResolver(resolver MailResendPolicyResolver) *PasswordResetService {
	if s != nil {
		s.resendPolicies = resolver
	}
	return s
}

func (s *PasswordResetService) WithLocaleResolver(resolver PasswordResetLocaleResolver) *PasswordResetService {
	if s != nil {
		s.localeResolver = resolver
	}
	return s
}

func (s *PasswordResetService) WithBrandResolver(resolver PasswordResetBrandResolver) *PasswordResetService {
	if s != nil {
		s.brandResolver = resolver
	}
	return s
}

// RequestPasswordResetInput 是发起密码重置的入参。
type RequestPasswordResetInput struct {
	Email, IP, Locale string
}

// RequestPasswordReset 查找用户并投递重置邮件。
// 出于隐私，无论邮箱是否存在都返回 nil；仅当邮件投递失败时返回错误（此时不暴露用户是否存在）。
// 调用方应始终返回相同的成功响应。
func (s *PasswordResetService) RequestPasswordReset(ctx context.Context, input RequestPasswordResetInput) error {
	_, err := s.RequestPasswordResetWithResult(ctx, input)
	return err
}

func (s *PasswordResetService) RequestPasswordResetWithResult(ctx context.Context, input RequestPasswordResetInput) (MailResendResult, error) {
	email := strings.TrimSpace(strings.ToLower(input.Email))
	if email == "" {
		return MailResendResult{}, nil
	}
	// 先限流再查库：对已知/未知邮箱统一按 email+IP 计数，避免枚举与邮件轰炸。
	retryAt, err := s.enforceRequestRateLimit(ctx, email, input.IP)
	if err != nil {
		return MailResendResult{}, err
	}
	result := MailResendResult{Sent: true, RetryAt: retryAt}
	// 先按登录名查凭据；external-only 用户无 credential 行时回退到用户查询。
	// 两种路径对外均非枚举：不存在/非活跃一律静默成功。
	userID, username, userLocale, status, found, err := s.lookupPasswordResetTarget(ctx, email)
	if err != nil {
		return result, err
	}
	if !found || status != UserStatusActive {
		return result, nil
	}

	rawToken, err := generateResetToken()
	if err != nil {
		return result, err
	}
	tokenHash := hashResetToken(rawToken)
	expiresAt := time.Now().Add(s.config.TokenTTL).UTC()

	ipHash := hashIP(input.IP)
	tokenInput := CreatePasswordResetTokenInput{
		UserID:        userID,
		TokenHash:     tokenHash,
		ExpiresAt:     expiresAt,
		RequestIPHash: ipHash,
	}
	if s.mailer == nil {
		_, err := s.store.CreatePasswordResetToken(ctx, tokenInput)
		return result, err
	}
	resetURL := s.buildResetURL(rawToken)
	brand := s.resolveMailBrand(ctx)
	message := PasswordResetMail{
		Recipient:      email,
		IdempotencyKey: "password_reset:" + tokenHash,
		Locale:         s.resolveMailLocale(ctx, input.Locale, userLocale),
		Username:       username,
		ResetURL:       resetURL,
		ExpiresAt:      expiresAt,
		SiteName:       brand.SiteName,
		Brand:          brand,
	}
	if err := s.mailer.QueuePasswordReset(ctx, tokenInput, message); err != nil {
		// 投递失败：记录但不向调用方暴露用户信息。
		// 注意：这里不返回错误，以免泄露"邮箱存在但投递失败"。
		// 调用方仍返回通用成功响应。
		return result, nil
	}
	return result, nil
}

func (s *PasswordResetService) resolveMailBrand(ctx context.Context) mail.Brand {
	if s != nil && s.brandResolver != nil {
		if brand, err := s.brandResolver.MailBrand(ctx); err == nil {
			return brand
		}
	}
	return mail.DefaultBrand(s.siteName(), s.config.SiteURL)
}

func (s *PasswordResetService) enforceRequestRateLimit(ctx context.Context, email, ip string) (time.Time, error) {
	policy, err := s.mailResendPolicy(ctx)
	if err != nil {
		return time.Time{}, err
	}
	return enforceMailResendPolicy(ctx, s.rateLimiter, "password_reset", hashResetToken(email), ip, policy, ErrPasswordResetRateLimited)
}

func (s *PasswordResetService) mailResendPolicy(ctx context.Context) (MailResendPolicy, error) {
	if s.resendPolicies != nil {
		return s.resendPolicies.MailResendPolicy(ctx)
	}
	return MailResendPolicy{
		Cooldown: s.config.RequestCooldown, Window: s.config.RequestWindow,
		MaxPerTarget: s.config.RequestMaxPerEmail, MaxPerIP: s.config.RequestMaxPerIP,
	}, nil
}

// ConfirmPasswordResetInput 是确认密码重置的入参。
type ConfirmPasswordResetInput struct {
	Token       string
	NewPassword string
}

// lookupPasswordResetTarget 解析重置目标：有密码凭据或 external-only 用户均可。
// found=false 表示不存在（调用方静默成功，不枚举邮箱）。
func (s *PasswordResetService) lookupPasswordResetTarget(
	ctx context.Context,
	email string,
) (userID int64, username, userLocale string, status UserStatus, found bool, err error) {
	credential, err := s.store.GetCredentialByLogin(ctx, email)
	if err == nil {
		return credential.ID, credential.Username, credential.Locale, credential.Status, true, nil
	}
	if !errors.Is(err, ErrCredentialNotFound) {
		return 0, "", "", "", false, err
	}
	// external-only：按邮箱加载用户（无凭据）。不存在同样视为 found=false。
	current, err := s.store.GetCurrentUserByEmail(ctx, email)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) || errors.Is(err, ErrCredentialNotFound) {
			return 0, "", "", "", false, nil
		}
		return 0, "", "", "", false, err
	}
	return current.ID, current.Username, current.Locale, current.Status, true, nil
}

// ConfirmPasswordReset 校验令牌、消费令牌、更新密码（事务原子完成）。
// external-only 用户确认时创建 password credential（见 ConfirmPasswordResetAtomic upsert）。
func (s *PasswordResetService) ConfirmPasswordReset(ctx context.Context, input ConfirmPasswordResetInput) error {
	token := strings.TrimSpace(input.Token)
	if token == "" {
		return ErrPasswordResetTokenNotFound
	}
	policy, err := s.passwordPolicies.PasswordPolicy(ctx)
	if err != nil {
		return err
	}
	if fields := policy.Validate(input.NewPassword); len(fields) > 0 {
		return NewRegisterInvalid(fields)
	}
	hash := hashResetToken(token)
	newHash, err := HashPassword(input.NewPassword)
	if err != nil {
		return err
	}
	// 令牌消费 + 改密 + token version + 撤销会话同一事务，避免中间态。
	_, err = s.store.ConfirmPasswordResetAtomic(ctx, hash, newHash, RevokeReasonPasswordReset)
	return err
}

func (s *PasswordResetService) buildResetURL(rawToken string) string {
	base := s.config.SiteURL
	if base == "" {
		base = "http://127.0.0.1:3000"
	}
	base = strings.TrimRight(base, "/")
	return base + s.config.ResetPathBase + rawToken
}

func (s *PasswordResetService) siteName() string {
	name := s.config.SiteName
	if name == "" {
		name = "SForum"
	}
	return name
}

func (s *PasswordResetService) resolveMailLocale(ctx context.Context, browserLocale, userLocale string) string {
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

// generateResetToken 生成 32 字节随机令牌并编码为 hex。
func generateResetToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate reset token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}

// hashResetToken 对令牌做 sha256 哈希，存储哈希值而非明文令牌。
func hashResetToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// hashIP 对请求 IP 做哈希，用于限流与审计，不存明文 IP。
func hashIP(ip string) string {
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}
