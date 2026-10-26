package pluginv2

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hashicorp/go-plugin"
	hostwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolwire "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	ErrHostUnavailable                = errors.New("protocol v2 host broker is unavailable")
	ErrHostActorDelegationUnavailable = errors.New("protocol v2 actor delegation is unavailable")
	ErrHostQueryDelegationUnavailable = errors.New("protocol v2 query delegation is unavailable")
	ErrHostCommandInvalidationInvalid = errors.New("protocol v2 Host Command query invalidation tags are invalid")
)

const (
	HostQueryOwnSettingsID         = "sforum.extensions.settings.own"
	HostQueryOwnSettingsVersion    = "1"
	HostQueryOwnSettingsSchemaID   = "sforum.extensions.settings.own.result"
	HostQueryOwnSettingsSchemaV1   = "1"
	HostQueryFilterValueSchemaRef  = "sforum.query.filter.value@1"
	hostCommandInvalidationMaxTags = 32
)

// Host exposes the generated Host API v2 clients on the runtime-scoped broker.
// Plugins should build every request context with RequestContext so the exact
// artifact identity and granted authority cannot drift from the handshake.
type Host struct {
	Queries     hostwire.HostQueryServiceClient
	Commands    hostwire.HostCommandServiceClient
	Database    hostwire.DatabaseServiceClient
	Cache       hostwire.CacheServiceClient
	Jobs        hostwire.JobServiceClient
	Schedules   hostwire.ScheduleServiceClient
	Services    hostwire.ServiceDiscoveryServiceClient
	Secrets     hostwire.SecretServiceClient
	Files       hostwire.FileServiceClient
	HTTP        hostwire.HttpServiceClient
	Admin       hostwire.AdminSurfaceServiceClient
	Identity    hostwire.IdentityServiceClient
	Permissions hostwire.PermissionServiceClient
	Media       hostwire.MediaServiceClient
	Navigation  hostwire.NavigationServiceClient
	Audit       hostwire.AuditServiceClient
	Tracing     hostwire.TracingServiceClient
	// PluginSession 是 S9 的宿主会话状态服务。插件只能通过
	// Host.SessionState(parent) 使用它：请求形状里没有扩展/用户/会话 id 字段。
	PluginSession hostwire.PluginSessionServiceClient

	conn      *grpc.ClientConn
	identity  *protocolwire.ExtensionIdentity
	authority []*protocolwire.AuthorityGrant
	instance  string
	sequence  atomic.Uint64
}

func newHost(
	broker *plugin.GRPCBroker,
	brokerID uint32,
	token []byte,
	identity *protocolwire.ExtensionIdentity,
	authority []*protocolwire.AuthorityGrant,
) (*Host, error) {
	if broker == nil || brokerID == 0 {
		return nil, ErrHostUnavailable
	}
	conn, err := broker.DialWithOptions(brokerID,
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(DefaultMaxMessageBytes),
			grpc.MaxCallSendMsgSize(DefaultMaxMessageBytes),
		),
		grpc.WithChainUnaryInterceptor(hostUnaryClientInterceptor(token)),
		grpc.WithChainStreamInterceptor(hostStreamClientInterceptor(token)),
	)
	if err != nil {
		return nil, err
	}
	host := &Host{
		conn: conn, identity: cloneIdentity(identity), authority: cloneAuthority(authority),
		instance: identity.GetInstanceId(),
	}
	host.Queries = hostwire.NewHostQueryServiceClient(conn)
	host.Commands = hostwire.NewHostCommandServiceClient(conn)
	host.Database = hostwire.NewDatabaseServiceClient(conn)
	host.Cache = hostwire.NewCacheServiceClient(conn)
	host.Jobs = hostwire.NewJobServiceClient(conn)
	host.Schedules = hostwire.NewScheduleServiceClient(conn)
	host.Services = hostwire.NewServiceDiscoveryServiceClient(conn)
	host.Secrets = hostwire.NewSecretServiceClient(conn)
	host.Files = hostwire.NewFileServiceClient(conn)
	host.HTTP = hostwire.NewHttpServiceClient(conn)
	host.Admin = hostwire.NewAdminSurfaceServiceClient(conn)
	host.Identity = hostwire.NewIdentityServiceClient(conn)
	host.Permissions = hostwire.NewPermissionServiceClient(conn)
	host.Media = hostwire.NewMediaServiceClient(conn)
	host.Navigation = hostwire.NewNavigationServiceClient(conn)
	host.Audit = hostwire.NewAuditServiceClient(conn)
	host.Tracing = hostwire.NewTracingServiceClient(conn)
	host.PluginSession = hostwire.NewPluginSessionServiceClient(conn)
	return host, nil
}

// RequestContext carries locale/trace data from an incoming plugin call while
// replacing runtime-owned fields. Actor is cleared until the Host provides an
// attested delegation token; plugin-supplied principals are never trusted.
func (h *Host) RequestContext(parent *protocolwire.RequestContext) *protocolwire.RequestContext {
	result := &protocolwire.RequestContext{}
	if parent != nil {
		result = proto.Clone(parent).(*protocolwire.RequestContext)
	}
	result.RequestId = h.instance + "-host-" + strconv.FormatUint(h.sequence.Add(1), 10)
	result.Actor = nil
	result.Extension = cloneIdentity(h.identity)
	result.GrantedAuthority = cloneAuthority(h.authority)
	// Delegation tokens are consumed only by the matching delegated request
	// helper and must not be copied onto arbitrary plugin-to-Host calls.
	result.HostCommandDelegations = nil
	result.HostQueryDelegations = nil
	result.HostSessionDelegation = nil
	if result.Locale == "" {
		result.Locale = "und"
	}
	maximum := time.Now().UTC().Add(DefaultRequestTimeout)
	if result.Deadline == nil || !result.Deadline.IsValid() || result.Deadline.AsTime().After(maximum) {
		result.Deadline = timestamppb.New(maximum)
	}
	return result
}

// DelegatedQueryRequest selects one exact Host-issued Query Registry token.
// Callers may add fields, relations, filters, sorts, and pagination to the
// returned request; actor/runtime authority remains sealed by the token.
func (h *Host) DelegatedQueryRequest(
	parent *protocolwire.RequestContext,
	queryID string,
	contractVersion string,
	planVersion string,
) (*hostwire.QueryRequest, error) {
	queryID = strings.TrimSpace(queryID)
	contractVersion = strings.TrimSpace(contractVersion)
	planVersion = strings.TrimSpace(planVersion)
	if h == nil || parent == nil || queryID == "" || contractVersion == "" || planVersion == "" {
		return nil, ErrHostQueryDelegationUnavailable
	}
	var matched *protocolwire.HostQueryDelegation
	for _, delegation := range parent.GetHostQueryDelegations() {
		if delegation == nil || delegation.GetQueryId() != queryID ||
			delegation.GetContractVersion() != contractVersion || delegation.GetPlanVersion() != planVersion {
			continue
		}
		if matched != nil {
			return nil, ErrHostQueryDelegationUnavailable
		}
		matched = delegation
	}
	if matched == nil || strings.TrimSpace(matched.GetToken()) == "" ||
		strings.TrimSpace(matched.GetResultSchemaId()) == "" || strings.TrimSpace(matched.GetResultSchemaVersion()) == "" {
		return nil, ErrHostQueryDelegationUnavailable
	}
	return &hostwire.QueryRequest{
		Context: h.RequestContext(parent), QueryId: queryID,
		ContractVersion: contractVersion, PlanVersion: planVersion,
		ResultSchemaId: matched.GetResultSchemaId(), ResultSchemaVersion: matched.GetResultSchemaVersion(),
		Scope: matched.GetScope(), ActorDelegation: matched.GetToken(),
	}, nil
}

// NewHostQueryFilterValue creates the only generic filter document accepted by
// the Query Registry outlet. The active query declaration still allowlists the
// field and provider-specific Host mapping validates the string value.
func NewHostQueryFilterValue(value string) (*protocolwire.TypedDocument, error) {
	return NewTypedDocument(HostQueryFilterValueSchemaRef, map[string]any{"value": value})
}

// DelegatedCommandRequest binds the one matching Host-issued token and
// idempotency key to a generated command request. Callers may set dry-run or an
// expected revision on the returned request before Plan or Execute.
func (h *Host) DelegatedCommandRequest(
	parent *protocolwire.RequestContext,
	commandID string,
	commandVersion string,
	input *protocolwire.TypedDocument,
) (*hostwire.CommandRequest, error) {
	commandID = strings.TrimSpace(commandID)
	commandVersion = strings.TrimSpace(commandVersion)
	if h == nil || parent == nil || commandID == "" || commandVersion == "" {
		return nil, ErrHostActorDelegationUnavailable
	}
	var matched *protocolwire.HostCommandDelegation
	for _, delegation := range parent.GetHostCommandDelegations() {
		if delegation == nil || delegation.GetCommandId() != commandID || delegation.GetCommandVersion() != commandVersion {
			continue
		}
		if matched != nil {
			return nil, ErrHostActorDelegationUnavailable
		}
		matched = delegation
	}
	if matched == nil || strings.TrimSpace(matched.GetToken()) == "" ||
		strings.TrimSpace(matched.GetIdempotencyKey()) == "" ||
		(parent.GetIdempotencyKey() != "" && parent.GetIdempotencyKey() != matched.GetIdempotencyKey()) {
		return nil, ErrHostActorDelegationUnavailable
	}
	requestContext := h.RequestContext(parent)
	requestContext.IdempotencyKey = matched.GetIdempotencyKey()
	request := &hostwire.CommandRequest{
		Context: requestContext, CommandId: commandID, CommandVersion: commandVersion,
		IdempotencyKey: matched.GetIdempotencyKey(), ActorDelegation: matched.GetToken(),
	}
	if input != nil {
		request.Input = proto.Clone(input).(*protocolwire.TypedDocument)
	}
	return request, nil
}

// BindCommandQueryInvalidationTags attaches a canonical owner-scoped semantic
// tag set to a Host Command request. The Host enqueues it atomically with the
// command write; callers must not send physical cache keys.
func (h *Host) BindCommandQueryInvalidationTags(request *hostwire.CommandRequest, tags ...string) error {
	if h == nil || h.identity == nil || request == nil || request.GetContext() == nil ||
		!proto.Equal(request.GetContext().GetExtension(), h.identity) ||
		len(tags) == 0 || len(tags) > hostCommandInvalidationMaxTags {
		return ErrHostCommandInvalidationInvalid
	}
	owner := strings.ToLower(strings.TrimSpace(h.identity.GetExtensionId()))
	canonical := make([]string, 0, len(tags))
	seen := make(map[string]struct{}, len(tags))
	for _, raw := range tags {
		tag := strings.ToLower(strings.TrimSpace(raw))
		if !queryRuntimeIDPattern.MatchString(tag) || !strings.HasPrefix(tag, owner+".") {
			return ErrHostCommandInvalidationInvalid
		}
		if _, duplicate := seen[tag]; duplicate {
			return ErrHostCommandInvalidationInvalid
		}
		seen[tag] = struct{}{}
		canonical = append(canonical, tag)
	}
	sort.Strings(canonical)
	request.QueryInvalidationTags = canonical
	return nil
}

func hostUnaryClientInterceptor(token []byte) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, request, reply any, conn *grpc.ClientConn, invoker grpc.UnaryInvoker, options ...grpc.CallOption) error {
		callCtx, cancel := hostCallContext(ctx, token)
		defer cancel()
		return invoker(callCtx, method, request, reply, conn, options...)
	}
}

func hostStreamClientInterceptor(token []byte) grpc.StreamClientInterceptor {
	return func(ctx context.Context, descriptor *grpc.StreamDesc, conn *grpc.ClientConn, method string, streamer grpc.Streamer, options ...grpc.CallOption) (grpc.ClientStream, error) {
		callCtx, cancel := hostCallContext(ctx, token)
		stream, err := streamer(callCtx, descriptor, conn, method, options...)
		if err != nil {
			cancel()
			return nil, err
		}
		return &hostClientStream{ClientStream: stream, cancel: cancel}, nil
	}
}

type hostClientStream struct {
	grpc.ClientStream
	cancel context.CancelFunc
}

func (s *hostClientStream) CloseSend() error {
	return s.ClientStream.CloseSend()
}

func (s *hostClientStream) RecvMsg(message any) error {
	err := s.ClientStream.RecvMsg(message)
	if err != nil {
		s.cancel()
	}
	return err
}

func hostCallContext(ctx context.Context, token []byte) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		ctx, cancel := context.WithCancel(ctx)
		return metadata.AppendToOutgoingContext(ctx, RuntimeTokenMetadataKey, string(token)), cancel
	}
	ctx, cancel := context.WithTimeout(ctx, DefaultRequestTimeout)
	return metadata.AppendToOutgoingContext(ctx, RuntimeTokenMetadataKey, string(token)), cancel
}

func cloneIdentity(value *protocolwire.ExtensionIdentity) *protocolwire.ExtensionIdentity {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolwire.ExtensionIdentity)
}

func cloneAuthority(values []*protocolwire.AuthorityGrant) []*protocolwire.AuthorityGrant {
	result := make([]*protocolwire.AuthorityGrant, 0, len(values))
	for _, value := range values {
		if value != nil {
			result = append(result, proto.Clone(value).(*protocolwire.AuthorityGrant))
		}
	}
	return result
}
