package humanverify

import (
	"context"
	"fmt"
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
// 所以「Options 读失败」时业务写入本身也成功不了。
func NewRuntimeService(source ConfigSource, store Store, fallback RuntimeConfig) *RuntimeService {
	return &RuntimeService{source: source, store: store, fallback: normalizeRuntimeConfig(fallback)}
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
	service, err := s.service(ctx)
	if err != nil {
		return Challenge{}, err
	}
	return service.Challenge(ctx, purpose, subject)
}

func (s *RuntimeService) Verify(ctx context.Context, req VerifyRequest) error {
	service, err := s.service(ctx)
	if err != nil {
		return err
	}
	return service.Verify(ctx, req)
}

func (s *RuntimeService) service(ctx context.Context) (*Service, error) {
	if s == nil {
		// typed-nil（把 (*RuntimeService)(nil) 装进 Verifier 接口）等价于「未接线」：
		// 与 NewDisabledService 同语义（零校验），绝不 panic。装配层因此不需要
		// 反射判空，也不会因为一个 nil 指针让发帖路径 500。
		return NewDisabledService(), nil
	}
	cfg := s.fallback
	if s.source != nil {
		if current, err := s.source.HumanVerificationConfig(ctx); err == nil {
			cfg = current
		}
	}
	return NewConfiguredService(cfg, s.store)
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
