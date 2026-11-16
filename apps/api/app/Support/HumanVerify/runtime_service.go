package humanverify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

type ConfigSource interface {
	HumanVerificationConfig(ctx context.Context) (RuntimeConfig, error)
}

type RuntimeService struct {
	source   ConfigSource
	store    Store
	fallback RuntimeConfig
	// logger 可选；缺省 slog.Default()。降级日志是唯一的可观测面（F-3），
	// 测试用探针 logger 断言它确实被调用。
	logger *slog.Logger
}

// NewRuntimeService 构造**生产注入类型**：每次 Verify/Challenge 都重新从 source 读
// 当前配置，因此运营改选项立即生效，不存在「进程启动时快照」的假开关。
//
// fallback 是 source 读取失败时的降级配置，语义必须明确（P3-3 审查要求写清）：
//   - fallback.Provider 为空/disabled → 该次请求**零校验**。这是 fail-open，前提是
//     「provider 只在 Options 里配置、env 没配」——此时 Options 读不出来，部署本来
//     就是未启用状态。
//   - fallback.Provider=altcha（env 配了 secret）→ fallback 的 PurposeEnabled 为 nil，
//     而 Service.purposeEnabled 对 nil map 返回 true，即**所有场景继续强制**。
//     这是 fail-closed：env 已经把该部署声明成「启用了人机验证」，Options 抖动不该
//     把它降级成零校验。
//
// 两种降级都不需要额外开关：真实故障下同一请求的写库也走同一个 PostgreSQL，
// 所以「Options 读失败」时业务写入本身也成功不了。但「写库也失败」不是必然
// （语句超时、连接池饱和、坏计划都可能只打中 Options 读取），所以降级本身必须
// 留痕：service() 每次读失败写一条结构化 warn，含 purpose、fallback 方向与原始
// err，见 knowledge/decisions/2026-11-17-human-verify-fallback-fail-open.md。
func NewRuntimeService(source ConfigSource, store Store, fallback RuntimeConfig) *RuntimeService {
	return &RuntimeService{source: source, store: store, fallback: normalizeRuntimeConfig(fallback)}
}

// WithLogger 注入降级日志使用的 logger（可选；nil 回落 slog.Default()）。
func (s *RuntimeService) WithLogger(logger *slog.Logger) *RuntimeService {
	if s == nil {
		return s
	}
	s.logger = logger
	return s
}

func NewConfiguredService(cfg RuntimeConfig, store Store) (*Service, error) {
	cfg = normalizeRuntimeConfig(cfg)
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", ProviderDisabled:
		return NewDisabledService(), nil
	case ProviderAltcha:
		if strings.TrimSpace(cfg.AltchaSecret) == "" {
			return nil, fmt.Errorf("altcha secret is required")
		}
		return NewService(
			ServiceConfig{
				Enabled:         true,
				ChallengeTTL:    cfg.AltchaTTL,
				EnabledPurposes: cfg.PurposeEnabled,
				RateLimit:       cfg.RateLimit,
				RateLimitWindow: cfg.RateLimitWindow,
			},
			NewAltchaProvider(AltchaConfig{
				Secret:       cfg.AltchaSecret,
				Cost:         cfg.AltchaCost,
				ChallengeTTL: cfg.AltchaTTL,
			}),
			store,
		), nil
	default:
		return nil, fmt.Errorf("unsupported human verification provider %q", cfg.Provider)
	}
}

func (s *RuntimeService) Challenge(ctx context.Context, purpose Purpose, subject Subject) (Challenge, error) {
	service, err := s.service(ctx, purpose)
	if err != nil {
		return Challenge{}, err
	}
	return service.Challenge(ctx, purpose, subject)
}

func (s *RuntimeService) Verify(ctx context.Context, req VerifyRequest) error {
	service, err := s.service(ctx, req.Purpose)
	if err != nil {
		return err
	}
	return service.Verify(ctx, req)
}

// service 解析本次请求实际使用的配置。
//
// 读取失败时**在这里**打唯一一条降级 warn，而不是每个调用点各打一条：调用点只
// 关心「用哪个配置」，降级方向与原始错误属于同一件事。日志必须能回答两个问题：
// 哪个场景（purpose）受影响、当前是继续强制还是零校验（fallback 方向）。
func (s *RuntimeService) service(ctx context.Context, purpose Purpose) (*Service, error) {
	if s == nil {
		// typed-nil（把 (*RuntimeService)(nil) 装进 Verifier 接口）等价于「未接线」：
		// 与 NewDisabledService 同语义（零校验），绝不 panic。装配层因此不需要
		// 反射判空，也不会因为一个 nil 指针让发帖路径 500。
		return NewDisabledService(), nil
	}
	cfg := s.fallback
	if s.source != nil {
		current, err := s.source.HumanVerificationConfig(ctx)
		if err != nil {
			logger := s.logger
			if logger == nil {
				logger = slog.Default()
			}
			logger.Warn("humanverify: read runtime config failed; using fallback",
				"purpose", string(purpose),
				"fallback", fallbackDirection(s.fallback),
				"error", err,
			)
		} else {
			cfg = current
		}
	}
	return NewConfiguredService(cfg, s.store)
}

// fallbackDirection 把 fallback 的降级方向写进日志，供运维区分「继续强制」与
// 「零校验」。fallback 已在构造时 normalize，Provider 只会是 altcha 或 disabled。
func fallbackDirection(cfg RuntimeConfig) string {
	if cfg.Provider == ProviderAltcha {
		return "altcha_enforced"
	}
	return "disabled_zero_verification"
}

func normalizeRuntimeConfig(cfg RuntimeConfig) RuntimeConfig {
	cfg.Provider = strings.ToLower(strings.TrimSpace(cfg.Provider))
	if cfg.Provider == "" {
		cfg.Provider = ProviderDisabled
	}
	cfg.AltchaSecret = strings.TrimSpace(cfg.AltchaSecret)
	if cfg.AltchaTTL <= 0 {
		cfg.AltchaTTL = 10 * time.Minute
	}
	if cfg.AltchaCost <= 0 {
		cfg.AltchaCost = 1000
	}
	if cfg.PurposeEnabled != nil {
		cfg.PurposeEnabled = copyPurposeMap(cfg.PurposeEnabled)
	}
	if cfg.RateLimitWindow <= 0 {
		cfg.RateLimitWindow = time.Minute
	}
	return cfg
}
