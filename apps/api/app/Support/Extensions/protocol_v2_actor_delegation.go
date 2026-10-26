package extensionsruntime

import (
	"context"
	"errors"
	"sort"
	"strings"

	extensions "github.com/keeptrue/sforum/apps/api/app/Models/Extensions"
	authsession "github.com/keeptrue/sforum/apps/api/app/Support/AuthSession"
	capabilities "github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostapi "github.com/keeptrue/sforum/apps/api/app/Support/HostAPI"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

var (
	ErrProtocolV2ActorDelegationInvalid       = errors.New("protocol v2 actor delegation is invalid")
	ErrProtocolV2ActorDelegationUnavailable   = errors.New("protocol v2 actor delegation is unavailable")
	ErrProtocolV2SessionDelegationUnavailable = errors.New("protocol v2 session delegation is unavailable")
)

// invocationRequestContext is used only by Host-owned route/admin transports.
// Jobs and other background calls continue to use requestContext directly and
// therefore cannot acquire an actor delegation.
//
// The returned retire function settles the S9 session delegation for this
// invocation and is never nil; callers must run it (defer) when the invocation
// ends, because that is what makes the delegation single-invocation.
func (c *protocolV2Client) invocationRequestContext(
	ctx context.Context,
	correlation string,
	actor *ProtocolV2InvocationActor,
	idempotencyKey string,
) (*protocolv2.RequestContext, func(), error) {
	requestContext := c.requestContext(ctx, correlation)
	if err := c.attachInvocationActorDelegation(ctx, requestContext, actor, idempotencyKey); err != nil {
		return nil, noopProtocolV2Retire, err
	}
	// 会话委派独立于 actor：匿名会话同样有会话状态（S5），因此这里不能挂在
	// actor != nil 的分支里。
	retire, err := c.attachInvocationSessionDelegation(ctx, requestContext)
	if err != nil {
		return nil, noopProtocolV2Retire, err
	}
	return requestContext, retire, nil
}

// attachInvocationActorDelegation seals the Host-authored actor plus its
// command delegations onto one invocation context.
func (c *protocolV2Client) attachInvocationActorDelegation(
	ctx context.Context,
	requestContext *protocolv2.RequestContext,
	actor *ProtocolV2InvocationActor,
	idempotencyKey string,
) error {
	if idempotencyKey != "" {
		if !validProtocolV2InvocationIdempotencyKey(idempotencyKey) {
			return ErrProtocolV2ActorDelegationInvalid
		}
		requestContext.IdempotencyKey = idempotencyKey
	}
	if actor == nil {
		return nil
	}
	if actor.UserID <= 0 {
		return ErrProtocolV2ActorDelegationInvalid
	}
	requestContext.Actor = &protocolv2.Actor{
		UserId: actor.UserID, PermissionKeys: append([]string(nil), actor.PermissionKeys...),
	}
	// 同意式读取的 query 委派与 command 委派互相独立：它不需要 idempotency key，
	// 也不要求 host_commands 授权，只要求 actor + 精确制品声明 users.email.read。
	if err := c.attachInvocationConsentQueryDelegation(ctx, requestContext, actor); err != nil {
		return err
	}
	if idempotencyKey == "" || !c.hostCommands {
		return nil
	}
	if c.delegations == nil {
		return ErrProtocolV2ActorDelegationUnavailable
	}
	grants, err := c.delegations.IssueProtocolV2ActorDelegations(ctx, hostapi.ProtocolV2ActorDelegationBundleRequest{
		ActorUserID: actor.UserID, PermissionKeys: append([]string(nil), actor.PermissionKeys...),
		Runtime: cloneV2Identity(c.identity), IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(ErrProtocolV2ActorDelegationUnavailable, err)
	}
	sort.Slice(grants, func(left, right int) bool {
		if grants[left].CommandID == grants[right].CommandID {
			return grants[left].CommandVersion < grants[right].CommandVersion
		}
		return grants[left].CommandID < grants[right].CommandID
	})
	seen := make(map[string]bool, len(grants))
	requestContext.HostCommandDelegations = make([]*protocolv2.HostCommandDelegation, 0, len(grants))
	for _, grant := range grants {
		commandID := strings.TrimSpace(grant.CommandID)
		commandVersion := strings.TrimSpace(grant.CommandVersion)
		key := commandID + "\x00" + commandVersion
		if commandID == "" || grant.CommandID != commandID || commandVersion == "" || grant.CommandVersion != commandVersion ||
			grant.IdempotencyKey != idempotencyKey || strings.TrimSpace(grant.Token) == "" || seen[key] {
			return ErrProtocolV2ActorDelegationInvalid
		}
		seen[key] = true
		requestContext.HostCommandDelegations = append(requestContext.HostCommandDelegations, &protocolv2.HostCommandDelegation{
			CommandId: grant.CommandID, CommandVersion: grant.CommandVersion,
			IdempotencyKey: grant.IdempotencyKey, Token: grant.Token,
		})
	}
	return nil
}

// noopProtocolV2Retire 让「没有会话委派」与「有委派」的调用方形状一致，
// 避免每个调用点都写一次 nil 判断。
func noopProtocolV2Retire() {}

// attachInvocationSessionDelegation mints the S9 session delegation for one
// Host-initiated plugin invocation and returns its retire function.
//
// Two gates, both deliberate:
//
//   - the scope must be on the Go context, which only the Host's HTTP dispatch
//     layer puts there. A background call (job, schedule, queue) therefore
//     cannot acquire a session delegation even though it uses the same client.
//   - a mint failure fails the invocation instead of silently running without
//     session state, because a plugin that loses state silently is worse than a
//     visible, retryable failure.
//
// The token itself carries no session identifier; the Host keeps the scope in
// its own mint ledger and reads it back at the service boundary.
func (c *protocolV2Client) attachInvocationSessionDelegation(
	ctx context.Context,
	requestContext *protocolv2.RequestContext,
) (func(), error) {
	if c == nil || requestContext == nil || ctx == nil {
		return noopProtocolV2Retire, nil
	}
	scope, ok := authsession.PluginSessionScopeFromContext(ctx)
	if !ok {
		return noopProtocolV2Retire, nil
	}
	if c.sessionDelegations == nil || c.identity == nil {
		return noopProtocolV2Retire, ErrProtocolV2SessionDelegationUnavailable
	}
	token, err := c.sessionDelegations.MintProtocolV2SessionDelegation(
		ctx, cloneV2Identity(c.identity), hostapi.ProtocolV2SessionScope{
			SessionFingerprint: scope.SessionFingerprint,
			IdleExpiresAt:      scope.IdleExpiresAt,
			AbsoluteExpiresAt:  scope.AbsoluteExpiresAt,
			ActorUserID:        scope.ActorUserID,
		},
	)
	if err != nil {
		if ctx.Err() != nil {
			return noopProtocolV2Retire, ctx.Err()
		}
		return noopProtocolV2Retire, errors.Join(ErrProtocolV2SessionDelegationUnavailable, err)
	}
	requestContext.HostSessionDelegation = &protocolv2.HostSessionDelegation{Token: token}
	issuer := c.sessionDelegations
	return func() { _ = issuer.RetireProtocolV2SessionDelegation(token) }, nil
}

// ValidateProtocolV2InvocationIdempotencyKey lets HTTP adapters reject a bad
// key before audit or plugin execution. Empty means the optional header was not
// supplied and remains valid.
func ValidateProtocolV2InvocationIdempotencyKey(value string) error {
	if value == "" || validProtocolV2InvocationIdempotencyKey(value) {
		return nil
	}
	return ErrProtocolV2ActorDelegationInvalid
}

func validProtocolV2InvocationIdempotencyKey(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

// 同意式读取（consent-gated）的 query actor 委派。
//
// 与 M4 shortcode 投影的差别只有 scope 与 query：shortcode 是 "content" 的
// 公开只读投影，这里是 "route" 的本人敏感字段读取，因此：
//   - 只有宿主发起的 route/admin 调用（invocationRequestContext）会走到这里；
//   - 只有精确制品**声明了 users.email.read** 才签发，绝不「先给委派再由查询拒绝」；
//   - 匿名调用拿不到委派（也没有 actor 可绑定）；
//   - 委派与调用同生命周期（一次性 token，Query Registry 侧消费即失效）。

// protocolV2ConsentQueryDelegationIssuerFor mirrors the shortcode/session issuer
// adapters: the production Gateway is the only object that may mint a query
// actor delegation.
func protocolV2ConsentQueryDelegationIssuerFor(
	registrar HostAPIRegistrar,
) hostapi.ProtocolV2QueryActorDelegationIssuer {
	if registrar == nil {
		return nil
	}
	result, _ := registrar.(hostapi.ProtocolV2QueryActorDelegationIssuer)
	return result
}

// protocolV2CapabilitySet 把已解析的能力条目转成运行时集合。它只用于宿主侧
// 决定「是否签发」，从不作为插件自证依据。
func protocolV2CapabilitySet(grants []extensions.CapabilityGrant) capabilities.Set {
	keys := make([]string, 0, len(grants))
	for _, grant := range grants {
		if key := strings.TrimSpace(grant.Key); key != "" {
			keys = append(keys, key)
		}
	}
	return capabilities.NewSet(keys)
}

// attachInvocationConsentQueryDelegation seals one self-email query delegation
// onto a Host-initiated invocation.
func (c *protocolV2Client) attachInvocationConsentQueryDelegation(
	ctx context.Context,
	requestContext *protocolv2.RequestContext,
	actor *ProtocolV2InvocationActor,
) error {
	if c == nil || requestContext == nil || actor == nil || actor.UserID <= 0 {
		// 匿名（含匿名会话）没有可绑定的 actor：不签发，也不报错——公开路由
		// 仍然正常工作，只是拿不到同意式读取。
		return nil
	}
	if !c.capabilities.Has(capabilities.UsersEmailRead) {
		return nil
	}
	if c.queryDelegations == nil || c.identity == nil {
		return ErrProtocolV2ActorDelegationUnavailable
	}
	grant, err := c.queryDelegations.IssueProtocolV2QueryActorDelegation(
		ctx,
		hostapi.ProtocolV2QueryActorDelegationRequest{
			ActorUserID: actor.UserID,
			Runtime:     cloneV2Identity(c.identity),
			QueryID:     hostapi.QuerySelfEmailRegistryID,
			Locale:      requestContext.GetLocale(),
			Scope:       hostapi.ProtocolV2ConsentQueryScope,
			MaxCost:     hostapi.ProtocolV2ConsentQueryMaxCost,
		},
	)
	if err != nil {
		if ctx != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.Join(ErrProtocolV2ActorDelegationUnavailable, err)
	}
	// 签发回来的委派必须正好是本次请求的合同：issuer 漂移按契约错误失败关闭。
	if grant.QueryID != hostapi.QuerySelfEmailRegistryID ||
		grant.ContractVersion != hostapi.QuerySelfEmailRegistryContractVersion ||
		grant.PlanVersion != hostapi.QuerySelfEmailRegistryPlanVersion {
		return ErrProtocolV2ActorDelegationInvalid
	}
	return appendProtocolV2HostQueryDelegation(requestContext, grant)
}

// appendProtocolV2HostQueryDelegation validates and appends one Host-issued
// query delegation, keeping the wire order deterministic (query id, then
// contract version) because the plugin matches entries positionally by id.
func appendProtocolV2HostQueryDelegation(
	requestContext *protocolv2.RequestContext,
	grant hostapi.ProtocolV2QueryActorDelegationGrant,
) error {
	queryID := strings.TrimSpace(grant.QueryID)
	contractVersion := strings.TrimSpace(grant.ContractVersion)
	planVersion := strings.TrimSpace(grant.PlanVersion)
	resultSchemaID := strings.TrimSpace(grant.ResultSchemaID)
	resultSchemaVersion := strings.TrimSpace(grant.ResultSchemaVersion)
	scope := strings.TrimSpace(grant.Scope)
	token := strings.TrimSpace(grant.Token)
	if requestContext == nil || queryID == "" || queryID != grant.QueryID ||
		contractVersion == "" || contractVersion != grant.ContractVersion ||
		planVersion == "" || planVersion != grant.PlanVersion ||
		resultSchemaID == "" || resultSchemaID != grant.ResultSchemaID ||
		resultSchemaVersion == "" || resultSchemaVersion != grant.ResultSchemaVersion ||
		scope == "" || scope != grant.Scope || token == "" || token != grant.Token {
		return ErrProtocolV2ActorDelegationInvalid
	}
	delegation := &protocolv2.HostQueryDelegation{
		QueryId: queryID, ContractVersion: contractVersion, PlanVersion: planVersion,
		ResultSchemaId: resultSchemaID, ResultSchemaVersion: resultSchemaVersion,
		Scope: scope, Token: token,
	}
	existing := requestContext.GetHostQueryDelegations()
	for _, item := range existing {
		if item == nil {
			return ErrProtocolV2ActorDelegationInvalid
		}
		if item.GetQueryId() == queryID {
			// 同一 invocation 内同一查询只允许一条委派：重复即视为契约漂移。
			return ErrProtocolV2ActorDelegationInvalid
		}
	}
	delegations := make([]*protocolv2.HostQueryDelegation, 0, len(existing)+1)
	delegations = append(delegations, existing...)
	delegations = append(delegations, delegation)
	sort.SliceStable(delegations, func(left, right int) bool {
		if delegations[left].GetQueryId() == delegations[right].GetQueryId() {
			return delegations[left].GetContractVersion() < delegations[right].GetContractVersion()
		}
		return delegations[left].GetQueryId() < delegations[right].GetQueryId()
	})
	requestContext.HostQueryDelegations = delegations
	return nil
}
