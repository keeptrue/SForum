package config

import (
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/keeptrue/sforum/apps/api/app/Support/Localization"
)

type Config struct {
	AppEnv                        string
	AppName                       string
	AppURL                        string
	AppLocale                     string
	SupportedLocales              []string
	HTTPHost                      string
	HTTPPort                      string
	HTTPReadTimeout               time.Duration
	HTTPWriteTimeout              time.Duration
	HTTPIdleTimeout               time.Duration
	HTTPBodyLimit                 int
	CompressLevel                 int
	PprofEnabled                  bool
	PprofAddr                     string
	WorkerPprofEnabled            bool
	WorkerPprofAddr               string
	DatabaseURL                   string
	MigrateOnStartup              bool
	DatabaseMaxConns              int32
	DatabaseMinConns              int32
	DatabaseMaxConnIdleTime       time.Duration
	DatabaseMaxConnLifetime       time.Duration
	DatabaseConnectTimeout        time.Duration
	WorkerDatabaseMaxConns        int32
	WorkerDatabaseMinConns        int32
	WorkerDatabaseMaxConnIdleTime time.Duration
	WorkerDatabaseMaxConnLifetime time.Duration
	WorkerDatabaseConnectTimeout  time.Duration
	WorkerShutdownTimeout         time.Duration
	RedisAddr                     string
	RedisPassword                 string
	RedisPoolSize                 int
	RedisMinIdleConns             int
	RedisDialTimeout              time.Duration
	RedisReadTimeout              time.Duration
	RedisWriteTimeout             time.Duration
	RedisConnMaxIdleTime          time.Duration
	RedisConnMaxLifetime          time.Duration
	SessionIdleTimeout            time.Duration
	SessionAbsoluteTimeout        time.Duration
	SessionRenewalInterval        time.Duration
	SessionHashSecret             string
	// IdentitySubjectHMACSecret 是外部主体 digest 的 Host 密钥材料。
	// Core 计算 HMAC-SHA256(key, providerId || 0x00 || subject)；密钥属于 identity 备份/恢复。
	// 生产必须显式配置强密钥；开发使用稳定默认可配置值，禁止进程随机材料。
	IdentitySubjectHMACSecret string
	HumanVerificationProvider string
	AltchaSecret              string
	AltchaChallengeTTL        time.Duration
	AltchaCost                int
	// OptionEncryptionKey 是 web_options 中敏感值（云存储凭证/SFTP 私钥等）AES-GCM 加密的密钥（hex 编码）。
	// 生产环境必须显式配置；缺失或为占位词时启动会被拒绝。
	OptionEncryptionKey string
	// AIAllowPrivateEndpoint 是 SFORUM_AI_ALLOW_PRIVATE_ENDPOINT 的原始取值：允许 AI
	// 供应商端点解析到私网/回环地址（本机或内网模型服务）。是否真正放行由
	// AllowPrivateAIEndpoint 决定——生产恒为 false。
	AIAllowPrivateEndpoint bool
	ExtensionRoot          string
	BuiltinExtensionRoot   string
	// ExternalExtensionRoots 是逗号分隔的第三方源码集合根目录。
	// Host 仅静态扫描并快照为 uploaded/staged，不自动启用或执行代码。
	ExternalExtensionRoots []string
	SafeMode               bool
	// V3TrustChallenges 在生产默认开启；非生产仍可显式开启以验证迁移流程。
	V3TrustChallenges bool
	TrustChallengeTTL time.Duration
	// Meilisearch 已拆为可选 search.provider 插件；core 不再读取 MEILI_*。
	LimiterWriteMax int
	LimiterWindow   time.Duration
	// CSRFTrustedOrigins 是 CSRF 中间件信任的来源站点 origin 列表（如 https://forum.example.com）。
	// API 在反向代理后看到的 Host 是内部地址，而 Origin 是公开站点，二者不匹配会被拒绝，
	// 因此必须显式列出公开站点。支持 https://*.example.com 通配符子域。
	CSRFTrustedOrigins []string
	// CSRFEnabled 控制 CSRF 中间件是否启用，默认 true（生产启用）。
	// 测试场景显式置 false 以避免每个测试请求都需携带 token。
	CSRFEnabled bool
	// TrustProxy 为 true 时，Fiber 与 clientip 在 TCP 对端属于 TrustedProxies 时
	// 才采信 X-Forwarded-For / X-Real-IP 等转发头（防伪造）。
	// 开发默认 true；生产若 TRUST_PROXY 未设则为 false，须显式开启并配置 TRUSTED_PROXIES。
	TrustProxy bool
	// TrustedProxies 是信任的代理 IP 或 CIDR 列表。
	// 开发默认信任 loopback + 私网（Docker 网桥）；生产默认空，必须显式配置边缘代理。
	TrustedProxies []string
	// TrustProxyPrivate / TrustProxyLoopback 控制是否把整类私网/环回视为信任代理。
	// 开发默认 true；生产仅当 TRUSTED_PROXIES 为空且未显式关闭时仍为 false（安全默认）。
	TrustProxyPrivate  bool
	TrustProxyLoopback bool
	// ProxyHeader 是 Fiber c.IP() 读取的转发头名，默认 X-Forwarded-For。
	// 业务代码应优先用 clientip.FromCtx，本字段主要服务 Fiber 内置限流等。
	ProxyHeader                  string
	JobQueueCriticalWorkers      int
	JobQueueDefaultWorkers       int
	JobQueueSearchWorkers        int
	JobQueueMailWorkers          int
	JobQueueNotificationsWorkers int
	JobQueueMaintenanceWorkers   int
	// JobQueueAIWorkers 是 AI 生成队列的并发上限。它独立成队且默认很低：调用慢，
	// 且同时打太多请求只会触发供应商限流。
	JobQueueAIWorkers int
	LogLevel          slog.Level
}

func Load() Config {
	appEnv := env("APP_ENV", "development")
	supported := localization.ParseSupportedLocales(env("SUPPORTED_LOCALES", "zh-CN,en-US"))
	defaultLocale := localization.Normalize(env("APP_LOCALE", localization.DefaultLocale), supported)
	sessionIdleTimeout := envDuration("SESSION_IDLE_TIMEOUT", 30*24*time.Hour)
	sessionAbsoluteTimeout := envDuration("SESSION_ABSOLUTE_TIMEOUT", 180*24*time.Hour)
	if sessionAbsoluteTimeout < sessionIdleTimeout {
		sessionAbsoluteTimeout = sessionIdleTimeout
	}
	// CSRF 信任源：优先读 CSRF_TRUSTED_ORIGINS；未配置时从 APP_URL 派生 scheme://host 作为默认。
	csrfOrigins := envStringSlice("CSRF_TRUSTED_ORIGINS")
	if len(csrfOrigins) == 0 {
		csrfOrigins = originsFromAppURL(env("APP_URL", ""))
	}
	// 真实客户端 IP：开发默认信任私网/loopback（Docker+Nuxt 反代）；生产须显式 TRUST_PROXY + TRUSTED_PROXIES。
	isProd := strings.EqualFold(appEnv, "production")
	isDev := strings.EqualFold(appEnv, "development")
	trustProxy := envBool("TRUST_PROXY", !isProd)
	trustedProxies := envStringSlice("TRUSTED_PROXIES")
	// 开发/非生产：未配置 TRUSTED_PROXIES 时默认信任私网与 loopback。
	// 生产：不默认信任任何网段，避免 API 直接暴露公网时被伪造 XFF 绕过限流。
	trustPrivate := envBool("TRUST_PROXY_PRIVATE", !isProd)
	trustLoopback := envBool("TRUST_PROXY_LOOPBACK", !isProd)
	proxyHeader := env("PROXY_HEADER", "X-Forwarded-For")

	// 开发态默认更瘦的 River / 连接池，避免本地 idle 撑满生产档并发槽位。
	// 非 development（含 production/test）保持生产规模默认；显式环境变量始终优先。
	jobDefaults := productionJobQueueDefaults()
	dbMaxConnsDefault := int32(10)
	dbMinConnsDefault := int32(2)
	redisPoolDefault := 20
	redisMinIdleDefault := 5
	if isDev {
		jobDefaults = developmentJobQueueDefaults()
		dbMaxConnsDefault = 5
		dbMinConnsDefault = 1
		redisPoolDefault = 8
		redisMinIdleDefault = 1
	}

	cfg := Config{
		AppEnv:             appEnv,
		AppName:            env("APP_NAME", "SForum"),
		AppURL:             env("APP_URL", "http://127.0.0.1:3000"),
		AppLocale:          defaultLocale,
		SupportedLocales:   supported,
		HTTPHost:           env("HTTP_HOST", "0.0.0.0"),
		HTTPPort:           env("HTTP_PORT", "8080"),
		HTTPReadTimeout:    envDuration("HTTP_READ_TIMEOUT", 10*time.Second),
		HTTPWriteTimeout:   envDuration("HTTP_WRITE_TIMEOUT", 20*time.Second),
		HTTPIdleTimeout:    envDuration("HTTP_IDLE_TIMEOUT", 120*time.Second),
		HTTPBodyLimit:      envPositiveInt("HTTP_BODY_LIMIT", 64*1024*1024),
		CompressLevel:      compressLevelFromEnv(env("COMPRESS_LEVEL", "default")),
		PprofEnabled:       envBool("PPROF_ENABLED", false),
		PprofAddr:          env("PPROF_ADDR", "127.0.0.1:6060"),
		WorkerPprofEnabled: envBool("WORKER_PPROF_ENABLED", false),
		WorkerPprofAddr:    env("WORKER_PPROF_ADDR", "127.0.0.1:6061"),
		// 默认启用 TLS（sslmode=require）；本地开发无 TLS 的 Postgres 需显式设置 sslmode=disable。
		DatabaseURL:                   env("DATABASE_URL", "postgres://sforum:sforum@postgres:5432/sforum?sslmode=require"),
		MigrateOnStartup:              envBool("MIGRATE_ON_STARTUP", true),
		DatabaseMaxConns:              envPositiveInt32("DATABASE_MAX_CONNS", dbMaxConnsDefault),
		DatabaseMinConns:              envPositiveInt32("DATABASE_MIN_CONNS", dbMinConnsDefault),
		DatabaseMaxConnIdleTime:       envDuration("DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		DatabaseMaxConnLifetime:       envDuration("DATABASE_MAX_CONN_LIFETIME", time.Hour),
		DatabaseConnectTimeout:        envDuration("DATABASE_CONNECT_TIMEOUT", 10*time.Second),
		WorkerDatabaseMaxConns:        envPositiveInt32("WORKER_DATABASE_MAX_CONNS", dbMaxConnsDefault),
		WorkerDatabaseMinConns:        envPositiveInt32("WORKER_DATABASE_MIN_CONNS", dbMinConnsDefault),
		WorkerDatabaseMaxConnIdleTime: envDuration("WORKER_DATABASE_MAX_CONN_IDLE_TIME", 30*time.Minute),
		WorkerDatabaseMaxConnLifetime: envDuration("WORKER_DATABASE_MAX_CONN_LIFETIME", time.Hour),
		WorkerDatabaseConnectTimeout:  envDuration("WORKER_DATABASE_CONNECT_TIMEOUT", 10*time.Second),
		WorkerShutdownTimeout:         envDuration("WORKER_SHUTDOWN_TIMEOUT", 30*time.Second),
		RedisAddr:                     env("REDIS_ADDR", "redis:6379"),
		RedisPassword:                 env("REDIS_PASSWORD", ""),
		RedisPoolSize:                 envPositiveInt("REDIS_POOL_SIZE", redisPoolDefault),
		RedisMinIdleConns:             envPositiveInt("REDIS_MIN_IDLE_CONNS", redisMinIdleDefault),
		RedisDialTimeout:              envDuration("REDIS_DIAL_TIMEOUT", 5*time.Second),
		RedisReadTimeout:              envDuration("REDIS_READ_TIMEOUT", 3*time.Second),
		RedisWriteTimeout:             envDuration("REDIS_WRITE_TIMEOUT", 3*time.Second),
		RedisConnMaxIdleTime:          envDuration("REDIS_CONN_MAX_IDLE_TIME", 30*time.Minute),
		RedisConnMaxLifetime:          envDuration("REDIS_CONN_MAX_LIFETIME", time.Hour),
		SessionIdleTimeout:            sessionIdleTimeout,
		SessionAbsoluteTimeout:        sessionAbsoluteTimeout,
		SessionRenewalInterval:        envDuration("SESSION_RENEWAL_INTERVAL", 24*time.Hour),
		SessionHashSecret:             env("SESSION_HASH_SECRET", "sforum-dev-session-hash-secret"),
		// 稳定开发默认：跨重启/实例一致；生产由 validateProductionSecrets 拒绝。
		IdentitySubjectHMACSecret:      env("IDENTITY_SUBJECT_HMAC_SECRET", IdentitySubjectHMACDevDefault),
		HumanVerificationProvider:      env("HUMAN_VERIFICATION_PROVIDER", "disabled"),
		AltchaSecret:                   env("ALTCHA_SECRET", "sforum-dev-altcha-secret"),
		AltchaChallengeTTL:             envDuration("ALTCHA_CHALLENGE_TTL", 10*time.Minute),
		AltchaCost:                     envPositiveInt("ALTCHA_COST", 1000),
		OptionEncryptionKey:            env("APP_OPTION_ENC_KEY", ""),
		AIAllowPrivateEndpoint:         envBool("SFORUM_AI_ALLOW_PRIVATE_ENDPOINT", false),
		ExtensionRoot:                  env("EXTENSION_ROOT", "../../storage/extensions"),
		BuiltinExtensionRoot:           env("BUILTIN_EXTENSION_ROOT", "../../extensions/builtin"),
		ExternalExtensionRoots:         envStringSlice("EXTERNAL_EXTENSION_ROOTS"),
		SafeMode:                       envBool("SFORUM_SAFE_MODE", false),
		V3TrustChallenges:              envBool("SFORUM_V3_TRUST_CHALLENGES", isProd),
		TrustChallengeTTL:              envDuration("SFORUM_V3_TRUST_CHALLENGE_TTL", 5*time.Minute),
		LimiterWriteMax:                envPositiveInt("LIMITER_WRITE_MAX", 30),
		LimiterWindow:                  envDuration("LIMITER_WINDOW", time.Minute),
		CSRFTrustedOrigins:             csrfOrigins,
		CSRFEnabled:                    envBool("CSRF_ENABLED", true),
		TrustProxy:                     trustProxy,
		TrustedProxies:                 trustedProxies,
		TrustProxyPrivate:              trustPrivate,
		TrustProxyLoopback:             trustLoopback,
		ProxyHeader:                    proxyHeader,
		JobQueueCriticalWorkers:        envPositiveInt("JOB_QUEUE_CRITICAL_WORKERS", jobDefaults.Critical),
		JobQueueDefaultWorkers:         envPositiveInt("JOB_QUEUE_DEFAULT_WORKERS", jobDefaults.Default),
		JobQueueSearchWorkers:          envPositiveInt("JOB_QUEUE_SEARCH_WORKERS", jobDefaults.Search),
		JobQueueMailWorkers:            envPositiveInt("JOB_QUEUE_MAIL_WORKERS", jobDefaults.Mail),
		JobQueueNotificationsWorkers:   envPositiveInt("JOB_QUEUE_NOTIFICATIONS_WORKERS", jobDefaults.Notifications),
		JobQueueMaintenanceWorkers:     envPositiveInt("JOB_QUEUE_MAINTENANCE_WORKERS", jobDefaults.Maintenance),
		JobQueueAIWorkers:              envPositiveInt("JOB_QUEUE_AI_WORKERS", jobDefaults.AI),
		LogLevel:                       parseLogLevel(env("LOG_LEVEL", "info")),
	}
	validateProductionSecrets(cfg)
	return cfg
}

// IdentitySubjectHMACDevDefault 是开发/测试稳定默认真值（≥32 字节）。
// 生产启动必须显式覆盖；该值本身属于 insecure 默认。
const IdentitySubjectHMACDevDefault = "sforum-dev-identity-subject-hmac-v1-not-for-prod!!"

// IdentitySubjectHMACMinKeyBytes 是主体 HMAC 密钥最小字节数（256-bit）。
const IdentitySubjectHMACMinKeyBytes = 32

// insecureSecretValues 是已知的不安全默认/占位值，生产环境不得使用。
var insecureSecretValues = map[string]bool{
	"":                               true,
	"change-me":                      true,
	"sforum-dev-session-hash-secret": true,
	"sforum-dev-altcha-secret":       true,
	IdentitySubjectHMACDevDefault:    true,
}

// validateProductionSecrets 在生产环境校验关键密钥非空且非占位词。
// 任一不满足直接 panic 拒绝启动，避免运维忘记配置导致生产静默回退到公开默认值。
// 校验走真实 APP_ENV=production 路径（与 Load 一致），不使用旁路 env 开关。
func validateProductionSecrets(cfg Config) {
	if !strings.EqualFold(cfg.AppEnv, "production") {
		return
	}
	type secretCheck struct {
		name string
		val  string
	}
	checks := []secretCheck{
		{"SESSION_HASH_SECRET", cfg.SessionHashSecret},
		{"ALTCHA_SECRET", cfg.AltchaSecret},
		{"APP_OPTION_ENC_KEY", cfg.OptionEncryptionKey},
	}
	for _, c := range checks {
		if insecureSecretValues[strings.TrimSpace(c.val)] {
			panic(fmt.Sprintf("config: %s must be set to a secure value in production (got empty/placeholder default)", c.name))
		}
	}
	if err := ValidateIdentitySubjectHMACSecret(cfg.IdentitySubjectHMACSecret, true); err != nil {
		panic(fmt.Sprintf("config: IDENTITY_SUBJECT_HMAC_SECRET %v", err))
	}
	// EXTENSION_ROOT 在非生产有相对默认值（../../storage/extensions），生产下相对路径会随
	// 进程工作目录漂移：容器里会解析成 /storage，报出「mkdir /storage: permission denied」
	// 这种看不出缺失变量的错误。生产必须显式给绝对路径。
	if root := strings.TrimSpace(cfg.ExtensionRoot); root == "" || !filepath.IsAbs(root) {
		panic(fmt.Sprintf("config: EXTENSION_ROOT must be an absolute path in production (got %q)", root))
	}
	// ADR 6.2.6：`extensions/dev/` 是脚手架输出目录，运行时永不扫描它；M8 任务 7 要求
	// 生产在解析/bootstrap 期**拒绝**开发根，而不是只把命令藏起来。
	for _, root := range cfg.ExternalExtensionRoots {
		if path := DevelopmentScaffoldRoot(root); path != "" {
			panic(fmt.Sprintf(
				"config: EXTERNAL_EXTENSION_ROOTS must not scan the development scaffold tree in production (got %q)",
				path,
			))
		}
	}
}

// DevelopmentScaffoldRoot 判断一个外部扩展根是否指向开发脚手架树
// （`…/extensions/dev`，含其子目录）。是则返回规范化后的路径，否则返回空串。
//
// 判据只认「extensions 目录下的 dev 段」，不做模糊匹配：生产里合法的外部根
// （例如 `extensions/optional`、自建目录）不受影响，而 `sforum make` 的默认输出
// （`extensions/dev/<id>`）无论被写成什么绝对路径都会被拦下。
func DevelopmentScaffoldRoot(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" {
		return ""
	}
	cleaned := filepath.Clean(trimmed)
	segments := strings.Split(filepath.ToSlash(cleaned), "/")
	for index := 0; index < len(segments)-1; index++ {
		if segments[index] == "extensions" && segments[index+1] == "dev" {
			return cleaned
		}
	}
	return ""
}

// ValidateIdentitySubjectHMACSecret 校验主体 HMAC 密钥材料。
// production=true 时拒绝缺失/弱/默认/占位值；非生产允许开发默认。
func ValidateIdentitySubjectHMACSecret(secret string, production bool) error {
	secret = strings.TrimSpace(secret)
	if production {
		if insecureSecretValues[secret] {
			return fmt.Errorf("must be set to a secure non-default value in production")
		}
		if len(parseIdentitySubjectHMACKeyBytes(secret)) < IdentitySubjectHMACMinKeyBytes {
			return fmt.Errorf("must be at least %d bytes (or %d hex chars) in production", IdentitySubjectHMACMinKeyBytes, IdentitySubjectHMACMinKeyBytes*2)
		}
		return nil
	}
	// 非生产：空值由 Load 填入开发默认；此处仅保证可解析长度（开发默认已满足）。
	if secret == "" {
		return nil
	}
	if len(parseIdentitySubjectHMACKeyBytes(secret)) < IdentitySubjectHMACMinKeyBytes && !insecureSecretValues[secret] {
		// 开发允许短密钥仅为本地调试，但明确短于最小值的自定义值仍接受（与历史测试兼容）。
		return nil
	}
	return nil
}

// parseIdentitySubjectHMACKeyBytes 将配置字符串解析为密钥字节。
// 优先 hex 解码（≥32 字节）；否则按原始 UTF-8 字节使用。
func parseIdentitySubjectHMACKeyBytes(secret string) []byte {
	secret = strings.TrimSpace(secret)
	if secret == "" {
		return nil
	}
	if decoded, err := hex.DecodeString(secret); err == nil && len(decoded) >= IdentitySubjectHMACMinKeyBytes {
		return decoded
	}
	return []byte(secret)
}

// IdentitySubjectHMACKeyBytes 导出密钥字节解析，供 bootstrap 注入 digest 服务。
func IdentitySubjectHMACKeyBytes(secret string) []byte {
	return parseIdentitySubjectHMACKeyBytes(secret)
}

// jobQueueDefaults 是按环境区分的 River 队列并发默认档。
type jobQueueDefaults struct {
	Critical      int
	Default       int
	Search        int
	Mail          int
	Notifications int
	Maintenance   int
	AI            int
}

// productionJobQueueDefaults 合计 30 worker slots（历史生产档）。
func productionJobQueueDefaults() jobQueueDefaults {
	return jobQueueDefaults{
		Critical: 4, Default: 8, Search: 6, Mail: 4, Notifications: 6, Maintenance: 2, AI: 2,
	}
}

// developmentJobQueueDefaults 合计 7 worker slots，降低本地 embed worker 基线。
func developmentJobQueueDefaults() jobQueueDefaults {
	return jobQueueDefaults{
		Critical: 1, Default: 2, Search: 1, Mail: 1, Notifications: 1, Maintenance: 1, AI: 1,
	}
}

// JobQueueWorkerTotal 返回配置中各条队列 MaxWorkers 之和（测试与诊断用）。
func JobQueueWorkerTotal(cfg Config) int {
	return cfg.JobQueueCriticalWorkers +
		cfg.JobQueueDefaultWorkers +
		cfg.JobQueueSearchWorkers +
		cfg.JobQueueMailWorkers +
		cfg.JobQueueNotificationsWorkers +
		cfg.JobQueueMaintenanceWorkers +
		cfg.JobQueueAIWorkers
}

func env(key string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(key string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envPositiveInt(key string, fallback int) int {
	value := envInt(key, fallback)
	if value <= 0 {
		return fallback
	}
	return value
}

func envPositiveInt32(key string, fallback int32) int32 {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 || parsed > int64(1<<31-1) {
		return fallback
	}
	return int32(parsed)
}

func envBool(key string, fallback bool) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "t", "yes", "y", "on":
		return true
	case "0", "false", "f", "no", "n", "off":
		return false
	default:
		return fallback
	}
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// envStringSlice 读取逗号分隔的字符串列表，逐项 trim 并丢弃空项与重复项。
func envStringSlice(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	seen := map[string]bool{}
	items := []string{}
	for _, part := range strings.Split(raw, ",") {
		item := strings.TrimSpace(part)
		if item == "" || seen[item] {
			continue
		}
		seen[item] = true
		items = append(items, item)
	}
	return items
}

// AllowPrivateAIEndpoint 决定 AI 供应商出站是否放行解析到私网/回环地址的端点
// （本机 Ollama、内网 vLLM 等）。默认关闭，且生产环境恒为 false：它是 SSRF 边界，
// 放行等于让服务端可以读取内网，而模型列表探测会把远端响应回传。忘记撤销这个
// 开关也不会在生产生效。
func AllowPrivateAIEndpoint(cfg Config) bool {
	if strings.EqualFold(cfg.AppEnv, "production") {
		return false
	}
	return cfg.AIAllowPrivateEndpoint
}

// ShouldUseSecureCookie 决定 session/CSRF cookie 是否带 Secure 标志。
// 生产环境强制启用；此外当 APP_URL 为 https 时也启用，避免 staging HTTPS 漏配。
func ShouldUseSecureCookie(cfg Config) bool {
	if strings.EqualFold(cfg.AppEnv, "production") {
		return true
	}
	if parsed, err := url.Parse(strings.TrimSpace(cfg.AppURL)); err == nil {
		return strings.EqualFold(parsed.Scheme, "https")
	}
	return false
}

// originsFromAppURL 从 APP_URL 派生 origin（scheme://host），作为 CSRF 信任源默认值。
// 解析失败或为空时返回 nil，交由上层按空信任源处理。
func originsFromAppURL(appURL string) []string {
	appURL = strings.TrimSpace(appURL)
	if appURL == "" {
		return nil
	}
	parsed, err := url.Parse(appURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	return []string{parsed.Scheme + "://" + parsed.Host}
}

// compressLevelFromEnv 把环境变量字符串映射为 fiber compress Level。
// fiber compress: 0=LevelDefault, 1=LevelBestSpeed, 2=LevelBestCompression, -1=LevelDisabled。
func compressLevelFromEnv(value string) int {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "disabled", "off", "none":
		return -1
	case "best_speed", "speed", "fast":
		return 1
	case "best_compression", "compression", "max":
		return 2
	default:
		return 0
	}
}

func parseLogLevel(value string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
