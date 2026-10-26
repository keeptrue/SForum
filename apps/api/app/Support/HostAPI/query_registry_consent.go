package hostapi

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	audit "github.com/keeptrue/sforum/apps/api/app/Support/Audit"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

const (
	// ProtocolV2ConsentQueryRateLimitMax 是 (actor, extension) 维度的读取上限：
	// 60 次/分钟。阈值按站点规模可调，但 Host 侧上限先按此默认值落地。
	ProtocolV2ConsentQueryRateLimitMax = 60
	// ProtocolV2ConsentQueryRateLimitWindow 是限流窗口。
	ProtocolV2ConsentQueryRateLimitWindow = time.Minute
	// ProtocolV2ConsentQueryAuditAction 是每次成功读取写入的稳定审计动作。
	// 它不带值：metadata 只有标识与行数。
	ProtocolV2ConsentQueryAuditAction = audit.ActionIdentityEmailConsentRead
	// protocolV2ConsentQueryRateLimitPrefix 让 consent 限流键与邮件/登录限流隔离。
	protocolV2ConsentQueryRateLimitPrefix = "email_claim:"
)

var (
	// ErrProtocolV2QueryCapabilityDenied 表示精确制品未声明该读取面的能力。
	ErrProtocolV2QueryCapabilityDenied = errors.New("hostapi: query capability is denied")
	// ErrProtocolV2QueryRateLimited 表示 (actor, extension) 超出同意式读取配额。
	ErrProtocolV2QueryRateLimited = errors.New("hostapi: consent query is rate limited")
	// ErrProtocolV2QueryConsentGateUnavailable 表示门控依赖（能力解析/审计/限流）
	// 不可用；此时一律 fail closed，不返回任何数据。
	ErrProtocolV2QueryConsentGateUnavailable = errors.New("hostapi: consent query gate is unavailable")
)

// ProtocolV2QueryCapabilityAuthority resolves the exact-artifact capability
// set. Implementations must fail while the artifact is not the active exact
// runtime; the capability is never trusted from the plugin request.
type ProtocolV2QueryCapabilityAuthority interface {
	AuthorizeProtocolV2QueryCapability(
		context.Context,
		*protocolv2.ExtensionIdentity,
		string,
	) error
}

// ProtocolV2QueryRateLimiter is the narrow counter boundary for consent reads.
// A nil limiter means "not configured" and allows the read (dev/test); callers
// never see a partial count.
type ProtocolV2QueryRateLimiter interface {
	Allow(ctx context.Context, key string, max int, window time.Duration) (bool, error)
}

// ProtocolV2ConsentQueryEvent is the audit payload for one successful consent
// read. It deliberately has no field for the returned values.
type ProtocolV2ConsentQueryEvent struct {
	ActorUserID      int64
	ExtensionID      string
	ExtensionVersion string
	ArtifactDigest   string
	QueryID          string
	PlanVersion      string
	RequestID        string
	Rows             int
}

// ProtocolV2QueryConsentGate is the Host-owned policy for ConsentGated
// definitions: exact-artifact capability, per-(actor, extension) rate limit,
// and one audit event per successful read. It owns no SQL and no cache.
type ProtocolV2QueryConsentGate struct {
	Capabilities ProtocolV2QueryCapabilityAuthority
	Auditor      audit.Writer
	Limiter      ProtocolV2QueryRateLimiter
	// Capability overrides the required capability key. Empty means
	// capabilities.UsersEmailRead.
	Capability string
	RateMax    int
	RateWindow time.Duration
}

func (g *ProtocolV2QueryConsentGate) requiredCapability() string {
	if g == nil || strings.TrimSpace(g.Capability) == "" {
		return capabilities.UsersEmailRead
	}
	return strings.TrimSpace(g.Capability)
}

func (g *ProtocolV2QueryConsentGate) rateMax() int {
	if g == nil || g.RateMax <= 0 {
		return ProtocolV2ConsentQueryRateLimitMax
	}
	return g.RateMax
}

func (g *ProtocolV2QueryConsentGate) rateWindow() time.Duration {
	if g == nil || g.RateWindow <= 0 {
		return ProtocolV2ConsentQueryRateLimitWindow
	}
	return g.RateWindow
}

// AuthorizeCapability checks only the exact-artifact capability. Issuance uses
// it so a plugin without the capability never receives a delegation at all.
func (g *ProtocolV2QueryConsentGate) AuthorizeCapability(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
) error {
	if g == nil || g.Capabilities == nil || ctx == nil {
		return ErrProtocolV2QueryConsentGateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := g.Capabilities.AuthorizeProtocolV2QueryCapability(ctx, identity, g.requiredCapability()); err != nil {
		return err
	}
	return nil
}

// Authorize enforces capability + the (actor, extension) rate limit before any
// row is read. Anonymous actors are denied: a consent read always has an actor.
func (g *ProtocolV2QueryConsentGate) Authorize(
	ctx context.Context,
	identity *protocolv2.ExtensionIdentity,
	actorUserID int64,
	queryID string,
) error {
	if actorUserID <= 0 {
		return ErrProtocolV2QueryCapabilityDenied
	}
	if err := g.AuthorizeCapability(ctx, identity); err != nil {
		return err
	}
	if g.Limiter == nil {
		return nil
	}
	key := protocolV2ConsentQueryRateLimitKey(actorUserID, identity.GetExtensionId(), queryID)
	allowed, err := g.Limiter.Allow(ctx, key, g.rateMax(), g.rateWindow())
	if err != nil {
		return errors.Join(ErrProtocolV2QueryConsentGateUnavailable, err)
	}
	if !allowed {
		return ErrProtocolV2QueryRateLimited
	}
	return nil
}

// Record appends the single audit event for one successful read. A missing
// auditor fails closed: the Host never returns consent data it cannot audit.
func (g *ProtocolV2QueryConsentGate) Record(ctx context.Context, event ProtocolV2ConsentQueryEvent) error {
	if g == nil || g.Auditor == nil {
		return ErrProtocolV2QueryConsentGateUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	metadata := map[string]any{
		"extension_id":      event.ExtensionID,
		"extension_version": event.ExtensionVersion,
		"artifact_digest":   event.ArtifactDigest,
		"query_id":          event.QueryID,
		"plan_version":      event.PlanVersion,
		"request_id":        event.RequestID,
		"rows":              event.Rows,
		"outcome":           string(QueryTraceAllowed),
	}
	if err := g.Auditor.Append(ctx, audit.Event{
		ActorUserID: event.ActorUserID,
		Action:      ProtocolV2ConsentQueryAuditAction,
		Metadata:    metadata,
	}); err != nil {
		return errors.Join(ErrProtocolV2QueryConsentGateUnavailable, err)
	}
	return nil
}

func protocolV2ConsentQueryRateLimitKey(actorUserID int64, extensionID string, queryID string) string {
	return protocolV2ConsentQueryRateLimitPrefix + strconv.FormatInt(actorUserID, 10) + ":" +
		strings.ToLower(strings.TrimSpace(extensionID)) + ":" + strings.ToLower(strings.TrimSpace(queryID))
}
