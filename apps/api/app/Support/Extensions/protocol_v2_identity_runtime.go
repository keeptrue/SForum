package extensionsruntime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	extensions "github.com/zhuchunshu/sforum/apps/api/app/Models/Extensions"
	"github.com/zhuchunshu/sforum/apps/api/app/Support/Capabilities"
	extensionmanifest "github.com/zhuchunshu/sforum/apps/api/app/Support/ExtensionManifest"
	hostapi "github.com/zhuchunshu/sforum/apps/api/app/Support/HostAPI"
	pluginv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2/gen/sforum/plugin/v2"
	protocolv2 "github.com/zhuchunshu/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	platformversion "github.com/zhuchunshu/sforum/apps/api/version"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
)

var ErrProtocolV2IdentityProviderInvalid = errors.New("protocol v2 identity provider invocation is invalid")

const protocolV2IdentityServerTimeSkew = 5 * time.Second

// InvokeIdentityProvider calls one exact executable Identity declaration. Host
// effects, Registry JSON Schema validation, and audit remain outside the
// transport so the Manager can keep its exact admission lease through them.
func (c *protocolV2Client) InvokeIdentityProvider(
	parent context.Context,
	input VersionedIdentityProviderRequest,
) (VersionedIdentityProviderResponse, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return VersionedIdentityProviderResponse{}, extensions.ErrRuntimeUnavailable
	}
	if err := parent.Err(); err != nil {
		return VersionedIdentityProviderResponse{}, err
	}
	if input.ActorUserID < 0 {
		return VersionedIdentityProviderResponse{}, fmt.Errorf("%w: actor user id", ErrProtocolV2IdentityProviderInvalid)
	}
	operation, err := c.exactManifestIdentityOperation(input)
	if err != nil {
		return VersionedIdentityProviderResponse{}, err
	}
	declaredTimeout := time.Duration(operation.TimeoutMS) * time.Millisecond
	if input.Timeout != declaredTimeout || declaredTimeout <= 0 || declaredTimeout > DefaultProtocolV2RequestTimeout {
		return VersionedIdentityProviderResponse{}, fmt.Errorf(
			"%w: provider %q timeout drifted", ErrProtocolV2IdentityProviderInvalid, input.ProviderID,
		)
	}
	inputSchemaID, inputSchemaVersion, err := protocolV2SchemaRef(input.InputSchemaWireReference)
	if err != nil {
		return VersionedIdentityProviderResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2IdentityProviderInvalid, err)
	}
	if _, _, err := protocolV2SchemaRef(input.OutputSchemaWireReference); err != nil {
		return VersionedIdentityProviderResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2IdentityProviderInvalid, err)
	}
	document, err := protocolV2Document(inputSchemaID, inputSchemaVersion, input.Input)
	if err != nil {
		return VersionedIdentityProviderResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2IdentityProviderInvalid, err)
	}

	ctx, cancel := protocolV2Deadline(parent, declaredTimeout)
	defer cancel()
	requestContext := c.identityRuntimeRequestContext(ctx, input.ActorUserID)
	startedAt := time.Now().UTC()
	response, err := c.client.ProviderCall(ctx, &pluginv2.ProviderCallRequest{
		Context: requestContext, SlotId: ProtocolV2IdentityProviderSlot,
		DeclarationId: input.ProviderID, ContractVersion: input.ContractVersion,
		Operation: input.Operation, Input: document,
	})
	receivedAt := time.Now().UTC()
	if err != nil {
		return VersionedIdentityProviderResponse{}, mapProtocolV2IdentityCallError(ctx, err)
	}
	if err := validateProtocolV2IdentityResponseContext(
		response.GetContext(), requestContext, startedAt, receivedAt,
	); err != nil {
		return VersionedIdentityProviderResponse{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return VersionedIdentityProviderResponse{}, err
	}
	if err := validateProtocolV2DocumentRef(
		response.GetOutput(), input.OutputSchemaWireReference, "identity provider output",
	); err != nil {
		return VersionedIdentityProviderResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2IdentityProviderInvalid, err)
	}
	return VersionedIdentityProviderResponse{Output: protocolV2Values(response.GetOutput())}, nil
}

func (c *protocolV2Client) exactManifestIdentityOperation(
	input VersionedIdentityProviderRequest,
) (extensions.ManifestIdentityProviderOperation, error) {
	if c.manifestIdentity == nil {
		return extensions.ManifestIdentityProviderOperation{}, fmt.Errorf(
			"%w: provider %q is not declared", ErrProtocolV2IdentityProviderInvalid, input.ProviderID,
		)
	}
	for _, provider := range c.manifestIdentity.Providers {
		if provider.ID != input.ProviderID || provider.ContractVersion != input.ContractVersion {
			continue
		}
		if provider.Kind != input.Kind || provider.Handler != input.Handler || provider.Priority != input.Priority {
			return extensions.ManifestIdentityProviderOperation{}, fmt.Errorf(
				"%w: provider %q declaration drifted", ErrProtocolV2IdentityProviderInvalid, input.ProviderID,
			)
		}
		for _, operation := range provider.Operations {
			if operation.Name != input.Operation {
				continue
			}
			if operation.InputSchema != input.InputSchema || operation.OutputSchema != input.OutputSchema ||
				operation.FailurePolicy != input.FailurePolicy {
				return extensions.ManifestIdentityProviderOperation{}, fmt.Errorf(
					"%w: provider %q operation %q drifted",
					ErrProtocolV2IdentityProviderInvalid, input.ProviderID, input.Operation,
				)
			}
			return operation, nil
		}
		return extensions.ManifestIdentityProviderOperation{}, fmt.Errorf(
			"%w: provider %q operation %q is not declared",
			ErrProtocolV2IdentityProviderInvalid, input.ProviderID, input.Operation,
		)
	}
	return extensions.ManifestIdentityProviderOperation{}, fmt.Errorf(
		"%w: provider %q is not declared", ErrProtocolV2IdentityProviderInvalid, input.ProviderID,
	)
}

// Identity calls carry no subprocess authority or raw request/session
// projection. A positive UserID is the only optional actor field.
func (c *protocolV2Client) identityRuntimeRequestContext(
	ctx context.Context,
	actorUserID int64,
) *protocolv2.RequestContext {
	request := c.requestContext(ctx, "identity.runtime")
	request.Trace = nil
	request.Actor = nil
	request.GrantedAuthority = nil
	request.IdempotencyKey = ""
	request.HostCommandDelegations = nil
	request.HostQueryDelegations = nil
	if actorUserID > 0 {
		request.Actor = &protocolv2.Actor{UserId: actorUserID}
	}
	return request
}

func validateProtocolV2IdentityResponseContext(
	response *protocolv2.ResponseContext,
	request *protocolv2.RequestContext,
	startedAt time.Time,
	receivedAt time.Time,
) error {
	if response == nil || request == nil || response.GetRequestId() != request.GetRequestId() ||
		!proto.Equal(response.GetTrace(), request.GetTrace()) ||
		!proto.Equal(response.GetExtension(), request.GetExtension()) {
		return fmt.Errorf("%w: response context does not match the exact runtime request", ErrProtocolV2IdentityProviderInvalid)
	}
	serverTime := response.GetServerTime()
	if serverTime == nil || !serverTime.IsValid() {
		return fmt.Errorf("%w: response server time is invalid", ErrProtocolV2IdentityProviderInvalid)
	}
	value := serverTime.AsTime()
	if value.Before(startedAt.Add(-protocolV2IdentityServerTimeSkew)) ||
		value.After(receivedAt.Add(protocolV2IdentityServerTimeSkew)) {
		return fmt.Errorf("%w: response server time is outside the request window", ErrProtocolV2IdentityProviderInvalid)
	}
	return nil
}

func mapProtocolV2IdentityCallError(ctx context.Context, err error) error {
	switch status.Code(err) {
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	case codes.Canceled:
		return context.Canceled
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// protocolV2HandshakeRequest 冻结一次握手的完整请求。宿主版本随第一帧一起下发：
// coreVersion 是唯一兼容权威，buildVersion 只用于识别 dev 构建与排障，永不参与
// 约束求值。握手是插件在发任何 RPC 之前就能读到版本的唯一时机。
func protocolV2HandshakeRequest(
	ctx context.Context,
	client *protocolV2Client,
	hostFeatures []*protocolv2.ProtocolFeature,
) *protocolv2.HandshakeRequest {
	identity := platformversion.Identity()
	return &protocolv2.HandshakeRequest{
		Context: client.requestContext(ctx, "handshake"),
		HostProtocols: []*protocolv2.ProtocolRange{{
			Protocol: protocolV2Name, Major: 2, MinMinor: 0, MaxMinor: 0,
		}},
		HostFeatures: hostFeatures,
		Limits: &protocolv2.RuntimeLimits{
			MaxReceiveBytes:         uint64(DefaultProtocolV2MaxMessageBytes),
			MaxSendBytes:            uint64(DefaultProtocolV2MaxMessageBytes),
			MaxConcurrentUnaryCalls: uint32(DefaultProtocolV2ConcurrentCalls),
			MaxConcurrentStreams:    uint32(DefaultProtocolV2ConcurrentCalls),
			DefaultDeadline:         durationpb.New(DefaultProtocolV2RequestTimeout),
			HealthDeadline:          durationpb.New(DefaultProtocolV2HandshakeTimeout),
			GracefulStopDeadline:    durationpb.New(5 * time.Second),
		},
		HostApiVersion: hostAPIV2Version,
		HostBrokerId:   client.hostBrokerID,
		RuntimeToken:   append([]byte(nil), client.token...),
		HostBuild: &protocolv2.HostBuild{
			CoreVersion: identity.CoreVersion, BuildVersion: identity.BuildVersion,
		},
	}
}

// protocolV2HostFeatures only offers runtime families required by the frozen
// Manifest declarations.
func protocolV2HostFeatures(
	queries []extensions.ManifestQuery,
	filters []extensions.ManifestQueryResultFilter,
	identity *extensions.ManifestIdentity,
	content []extensions.ManifestContent,
) []*protocolv2.ProtocolFeature {
	features := []*protocolv2.ProtocolFeature{
		{Name: "stream.routes", Version: "1"},
		{Name: "stream.files", Version: "1"},
		{Name: "stream.jobs", Version: "1"},
		{Name: "service.discovery", Version: "1"},
	}
	if protocolV2RequiresQueryRuntime(queries, filters) {
		features = append(features, &protocolv2.ProtocolFeature{
			Name: protocolV2QueryRuntimeFeatureName, Version: protocolV2QueryRuntimeFeatureVersion, Required: true,
		})
	}
	if protocolV2RequiresIdentityRuntime(identity) {
		features = append(features, &protocolv2.ProtocolFeature{
			Name: protocolV2IdentityRuntimeFeatureName, Version: protocolV2IdentityRuntimeFeatureVersion, Required: true,
		})
	}
	if protocolV2RequiresContentRuntime(content) {
		features = append(features, &protocolv2.ProtocolFeature{
			Name: protocolV2ContentRuntimeFeatureName, Version: protocolV2ContentRuntimeFeatureVersion,
		})
	}
	return features
}

func protocolV2RequiresContentRuntime(content []extensions.ManifestContent) bool {
	for _, declaration := range content {
		if strings.TrimSpace(declaration.Handler) != "" {
			return true
		}
	}
	return false
}

var ErrProtocolV2ContentInvalid = errors.New("protocol v2 content invocation is invalid")

const protocolV2ContentServerTimeSkew = 5 * time.Second

func (c *protocolV2Client) InvokeContent(
	parent context.Context,
	input VersionedContentRequest,
) (VersionedContentResponse, error) {
	if c == nil || c.client == nil || c.identity == nil || parent == nil {
		return VersionedContentResponse{}, extensions.ErrRuntimeUnavailable
	}
	if err := parent.Err(); err != nil {
		return VersionedContentResponse{}, err
	}
	if !c.exactManifestContent(input) {
		return VersionedContentResponse{}, fmt.Errorf("%w: declaration %q drifted", ErrProtocolV2ContentInvalid, input.DeclarationID)
	}
	if input.Timeout <= 0 || input.Timeout > DefaultProtocolV2RequestTimeout || !validProtocolV2ContentOperation(input.Operation) {
		return VersionedContentResponse{}, fmt.Errorf("%w: operation or timeout", ErrProtocolV2ContentInvalid)
	}
	schemaID, schemaVersion, err := protocolV2SchemaRef(ProtocolV2ContentRequestSchema)
	if err != nil {
		return VersionedContentResponse{}, err
	}
	document, err := protocolV2Document(schemaID, schemaVersion, input.Input)
	if err != nil {
		return VersionedContentResponse{}, err
	}
	ctx, cancel := protocolV2Deadline(parent, input.Timeout)
	defer cancel()
	requestContext, err := c.contentRuntimeRequestContext(ctx, input.DeclarationID, input.ActorUserID, input.SuppressHostDelegations)
	if err != nil {
		return VersionedContentResponse{}, err
	}
	startedAt := time.Now().UTC()
	response, err := c.client.ProviderCall(ctx, &pluginv2.ProviderCallRequest{
		Context: requestContext, SlotId: ProtocolV2ContentProviderSlot,
		DeclarationId: input.DeclarationID, ContractVersion: input.ContractVersion,
		Operation: input.Operation, Input: document,
	})
	receivedAt := time.Now().UTC()
	if err != nil {
		switch status.Code(err) {
		case codes.DeadlineExceeded:
			return VersionedContentResponse{}, context.DeadlineExceeded
		case codes.Canceled:
			return VersionedContentResponse{}, context.Canceled
		}
		if ctx.Err() != nil {
			return VersionedContentResponse{}, ctx.Err()
		}
		return VersionedContentResponse{}, err
	}
	if err := validateProtocolV2ContentResponseContext(response.GetContext(), requestContext, startedAt, receivedAt); err != nil {
		return VersionedContentResponse{}, err
	}
	if err := protocolV2Error(response.GetError()); err != nil {
		return VersionedContentResponse{}, err
	}
	if err := validateProtocolV2DocumentRef(response.GetOutput(), ProtocolV2ContentResponseSchema, "content output"); err != nil {
		return VersionedContentResponse{}, fmt.Errorf("%w: %v", ErrProtocolV2ContentInvalid, err)
	}
	return VersionedContentResponse{Output: protocolV2Values(response.GetOutput())}, nil
}

func (c *protocolV2Client) exactManifestContent(input VersionedContentRequest) bool {
	for _, declaration := range c.content {
		if declaration.ID == input.DeclarationID && declaration.ContractVersion == input.ContractVersion &&
			declaration.Kind == input.Kind && declaration.Handler == input.Handler && declaration.Schema == input.Schema &&
			declaration.Renderer == input.Renderer && declaration.Migration == input.Migration &&
			strings.TrimSpace(declaration.Handler) != "" {
			return true
		}
	}
	return false
}

func (c *protocolV2Client) contentRuntimeRequestContext(
	ctx context.Context,
	declarationID string,
	actorUserID int64,
	suppressHostDelegations ...bool,
) (*protocolv2.RequestContext, error) {
	request := c.requestContext(ctx, declarationID)
	request.Trace = nil
	request.Actor = nil
	request.GrantedAuthority = nil
	request.IdempotencyKey = ""
	request.HostCommandDelegations = nil
	request.HostQueryDelegations = nil
	if len(suppressHostDelegations) > 0 && suppressHostDelegations[0] {
		return request, nil
	}
	if c.identity.GetExtensionId() != hostapi.ShortcodeProjectionExtensionID {
		return request, nil
	}
	if c.shortcodeQueries == nil || actorUserID < 0 {
		return nil, ErrProtocolV2ContentInvalid
	}
	grants, err := c.shortcodeQueries.IssueProtocolV2ShortcodeProjectionDelegations(
		ctx,
		hostapi.ProtocolV2ShortcodeProjectionDelegationRequest{
			ActorUserID: actorUserID, Runtime: cloneV2Identity(c.identity), Locale: request.Locale, Scope: "content",
		},
	)
	if err != nil {
		return nil, errors.Join(ErrProtocolV2ContentInvalid, err)
	}
	request.HostQueryDelegations = make([]*protocolv2.HostQueryDelegation, 0, len(grants))
	for _, grant := range grants {
		request.HostQueryDelegations = append(request.HostQueryDelegations, &protocolv2.HostQueryDelegation{
			QueryId: grant.QueryID, ContractVersion: grant.ContractVersion, PlanVersion: grant.PlanVersion,
			ResultSchemaId: grant.ResultSchemaID, ResultSchemaVersion: grant.ResultSchemaVersion,
			Scope: grant.Scope, Token: grant.Token,
		})
	}
	return request, nil
}

func validateProtocolV2ContentResponseContext(
	response *protocolv2.ResponseContext,
	request *protocolv2.RequestContext,
	startedAt, receivedAt time.Time,
) error {
	if response == nil || request == nil || response.GetRequestId() != request.GetRequestId() ||
		!proto.Equal(response.GetTrace(), request.GetTrace()) || !proto.Equal(response.GetExtension(), request.GetExtension()) {
		return fmt.Errorf("%w: response context does not match exact runtime", ErrProtocolV2ContentInvalid)
	}
	serverTime := response.GetServerTime()
	if serverTime == nil || !serverTime.IsValid() {
		return fmt.Errorf("%w: response server time", ErrProtocolV2ContentInvalid)
	}
	value := serverTime.AsTime()
	if value.Before(startedAt.Add(-protocolV2ContentServerTimeSkew)) ||
		value.After(receivedAt.Add(protocolV2ContentServerTimeSkew)) {
		return fmt.Errorf("%w: response server time window", ErrProtocolV2ContentInvalid)
	}
	return nil
}

func validProtocolV2ContentOperation(value string) bool {
	switch value {
	case "editor", "validator", "serializer", "renderer", "filter":
		return true
	default:
		return false
	}
}

func protocolV2ActorDelegationBundleIssuerFor(registrar HostAPIRegistrar) hostapi.ProtocolV2ActorDelegationBundleIssuer {
	if registrar == nil {
		return nil
	}
	result, _ := registrar.(hostapi.ProtocolV2ActorDelegationBundleIssuer)
	return result
}

// protocolV2SessionDelegationIssuerFor mirrors the two delegation issuers above:
// the production Gateway implements the S9 capability and is the only object
// that may mint a session delegation.
func protocolV2SessionDelegationIssuerFor(registrar HostAPIRegistrar) hostapi.ProtocolV2SessionDelegationIssuer {
	if registrar == nil {
		return nil
	}
	result, _ := registrar.(hostapi.ProtocolV2SessionDelegationIssuer)
	return result
}

func protocolV2ShortcodeProjectionDelegationIssuerFor(
	registrar HostAPIRegistrar,
) hostapi.ProtocolV2ShortcodeProjectionDelegationBundleIssuer {
	if registrar == nil {
		return nil
	}
	result, _ := registrar.(hostapi.ProtocolV2ShortcodeProjectionDelegationBundleIssuer)
	return result
}

// Host 侧的身份/权限投影与克隆助手。原先住在 protocol_v2_client.go，该文件已顶到
// 自己的大小基线；这些函数只做投影与深拷贝，与 identity runtime 同属一层。
func protocolV2Authority(grants []extensions.CapabilityGrant) []*protocolv2.AuthorityGrant {
	result := make([]*protocolv2.AuthorityGrant, 0, len(grants))
	for _, grant := range grants {
		source := "declared"
		if grant.Implied {
			source = "host-implied"
		}
		result = append(result, &protocolv2.AuthorityGrant{
			Key: grant.Key, ContractVersion: hostAPIV2Version,
			RiskTier: protocolV2Risk(grant.Risk), Source: source,
		})
	}
	return result
}

func protocolV2Risk(risk string) protocolv2.RiskTier {
	switch risk {
	case capabilities.RiskLow:
		return protocolv2.RiskTier_RISK_TIER_LOW
	case capabilities.RiskMedium:
		return protocolv2.RiskTier_RISK_TIER_MEDIUM
	case capabilities.RiskHigh:
		return protocolv2.RiskTier_RISK_TIER_HIGH
	default:
		return protocolv2.RiskTier_RISK_TIER_UNSPECIFIED
	}
}

func cloneV2Identity(value *protocolv2.ExtensionIdentity) *protocolv2.ExtensionIdentity {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.ExtensionIdentity)
}

func cloneManifestIdentity(value *extensions.ManifestIdentity) *extensions.ManifestIdentity {
	if value == nil {
		return nil
	}
	result := *value
	result.UserFields = append([]extensionmanifest.ManifestIdentityUserField(nil), value.UserFields...)
	result.Providers = append([]extensions.ManifestIdentityProvider(nil), value.Providers...)
	for index := range result.Providers {
		result.Providers[index].Operations = append(
			[]extensions.ManifestIdentityProviderOperation(nil), value.Providers[index].Operations...,
		)
	}
	result.RiskHooks = append([]string(nil), value.RiskHooks...)
	return &result
}

func cloneV2Authority(values []*protocolv2.AuthorityGrant) []*protocolv2.AuthorityGrant {
	result := make([]*protocolv2.AuthorityGrant, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, proto.Clone(value).(*protocolv2.AuthorityGrant))
		}
	}
	return result
}
