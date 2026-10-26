package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

// 同意式读取（consent-gated）的生产门控装配。
//
// 三件事都归宿主：
//   - 能力：来自精确制品的 manifest（并与启用影响评审逐项比对），不由请求自证；
//   - 限流：Redis 计数器，键按 (actor, extension, query) 隔离；
//   - 审计：每次成功读取一条 audit_events，不含任何字段值。

// productionQueryCapabilityAuthority 只信任精确制品解析结果。解析失败按
// stale/不可用分开上抛，绝不把「解析不出来」当成「有权限」。
type productionQueryCapabilityAuthority struct {
	resolver hostapi.ProtocolV2QueryAuthorityResolver
}

func (a *productionQueryCapabilityAuthority) AuthorizeProtocolV2QueryCapability(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	capability string,
) error {
	capability = strings.TrimSpace(capability)
	if a == nil || a.resolver == nil || ctx == nil || capability == "" {
		return hostapi.ErrProtocolV2QueryCapabilityDenied
	}
	authority, err := a.resolver.ResolveProtocolV2QueryAuthority(ctx, identity)
	if err != nil {
		if errors.Is(err, hostapi.ErrProtocolV2QueryRuntimeStale) {
			return err
		}
		return errors.Join(hostapi.ErrProtocolV2QueryConsentGateUnavailable, err)
	}
	if !authority.ExactArtifact {
		return hostapi.ErrProtocolV2QueryRuntimeStale
	}
	for _, key := range authority.Capabilities {
		if key == capability {
			return nil
		}
	}
	return hostapi.ErrProtocolV2QueryCapabilityDenied
}

// productionQueryConsentLimiter 是 consent 读取的最小 Redis 计数器。未配置
// Redis 时返回 nil（宿主按既有 limiter 语义放行），绝不静默改成别的键空间。
type productionQueryConsentLimiter struct {
	client *redis.Client
}

const productionQueryConsentLimitPrefix = "sforum:hostquery:"

func newProductionQueryConsentLimiter(client *redis.Client) hostapi.ProtocolV2QueryRateLimiter {
	if client == nil {
		return nil
	}
	return &productionQueryConsentLimiter{client: client}
}

func (l *productionQueryConsentLimiter) Allow(
	ctx context.Context,
	key string,
	max int,
	window time.Duration,
) (bool, error) {
	if l == nil || l.client == nil || key == "" || max <= 0 || window <= 0 {
		return true, nil
	}
	redisKey := productionQueryConsentLimitPrefix + key
	count, err := l.client.Incr(ctx, redisKey).Result()
	if err != nil {
		return false, fmt.Errorf("consent query rate incr: %w", err)
	}
	if count == 1 {
		if err := l.client.Expire(ctx, redisKey, window).Err(); err != nil {
			return false, fmt.Errorf("consent query rate expire: %w", err)
		}
	}
	return int(count) <= max, nil
}

// newProductionQueryConsentGate composes the three Host-owned boundaries for
// ConsentGated definitions. A nil resolver keeps the gate fail closed.
func newProductionQueryConsentGate(
	resolver hostapi.ProtocolV2QueryAuthorityResolver,
	auditor audit.Writer,
	client *redis.Client,
) *hostapi.ProtocolV2QueryConsentGate {
	return &hostapi.ProtocolV2QueryConsentGate{
		Capabilities: &productionQueryCapabilityAuthority{resolver: resolver},
		Auditor:      auditor,
		Limiter:      newProductionQueryConsentLimiter(client),
	}
}
