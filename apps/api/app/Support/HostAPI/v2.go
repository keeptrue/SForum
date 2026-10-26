package hostapi

import (
	"context"
	"strings"
	"time"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	VersionV2                = "sforum.host/v2"
	QueryOwnSettingsID       = "sforum.extensions.settings.own"
	QueryOwnSettingsVersion  = "1"
	QueryOwnSettingsSchemaID = "sforum.extensions.settings.own.result"
	QueryOwnSettingsSchemaV1 = "1"
	// QueryExtensionInventoryID 是 extensions.read 对应的稳定去敏清单查询。
	QueryExtensionInventoryID       = "sforum.core.extensions.inventory.list"
	QueryExtensionInventoryVersion  = "1"
	QueryExtensionInventorySchemaID = "sforum.core.extensions.inventory"
	QueryExtensionInventorySchemaV1 = "1"
	IdentitySafeUserSchemaID        = "sforum.identity.user.safe"
	IdentitySafeUserSchemaV1        = "1"
	PermissionPolicyID              = "sforum.identity.rbac@1"
	JobPayloadSchemaVersionV1       = "1"
	AuditMetadataSchemaVersion      = "1"
)

type protocolV2Core struct {
	service       *Service
	services      *ServiceRegistry
	commands      *protocolV2CommandEngine
	queries       *protocolV2QueryEngine
	queryRegistry *ProtocolV2QueryRegistryService
	database      *protocolV2DatabaseEngine
	providers     ProtocolV2ProviderBroker
}

func registerProtocolV2(
	server grpc.ServiceRegistrar,
	service *Service,
	services *ServiceRegistry,
	commands *protocolV2CommandEngine,
	queries *protocolV2QueryEngine,
	queryRegistry *ProtocolV2QueryRegistryService,
	database *protocolV2DatabaseEngine,
	cache *ProtocolV2CacheServiceServer,
	secrets *ProtocolV2SecretServiceServer,
	files *ProtocolV2FileServiceServer,
	httpClient *ProtocolV2HttpServiceServer,
	session *ProtocolV2SessionServiceServer,
	providers ProtocolV2ProviderBroker,
) {
	core := &protocolV2Core{
		service: service, services: services, commands: commands, queries: queries,
		queryRegistry: queryRegistry, database: database, providers: providers,
	}
	hostv2.RegisterHostQueryServiceServer(server, &protocolV2QueryServer{core: core})
	hostv2.RegisterHostCommandServiceServer(server, &protocolV2CommandServer{core: core})
	hostv2.RegisterDatabaseServiceServer(server, &protocolV2DatabaseServer{engine: core.database})
	hostv2.RegisterPermissionServiceServer(server, &protocolV2PermissionServer{core: core})
	hostv2.RegisterIdentityServiceServer(server, &protocolV2IdentityServer{core: core})
	hostv2.RegisterJobServiceServer(server, &protocolV2JobServer{core: core})
	hostv2.RegisterAuditServiceServer(server, &protocolV2AuditServer{core: core})
	hostv2.RegisterServiceDiscoveryServiceServer(server, &protocolV2ServiceDiscoveryServer{core: core})
	if cache != nil {
		hostv2.RegisterCacheServiceServer(server, cache)
	}
	if secrets != nil {
		hostv2.RegisterSecretServiceServer(server, secrets)
	}
	if files != nil {
		hostv2.RegisterFileServiceServer(server, files)
	}
	if httpClient != nil {
		hostv2.RegisterHttpServiceServer(server, httpClient)
	}
	// S9: the session-state service is registered only when the production
	// assembly bound it. Until then the RPC answers Unimplemented, which is the
	// honest state — a declared-but-unserved capability is what this program
	// forbids.
	if session != nil {
		hostv2.RegisterPluginSessionServiceServer(server, session)
	}
}

func (c *protocolV2Core) call(ctx context.Context, requestContext *protocolv2.RequestContext, method string, payload map[string]any) Response {
	if c == nil || c.service == nil {
		return fail("host.unavailable", "Host API is not configured.")
	}
	extensionID := strings.TrimSpace(requestContext.GetExtension().GetExtensionId())
	return c.service.call(ctx, Request{Method: method, ExtensionID: extensionID, Payload: payload}, VersionV2)
}

func protocolV2ResponseContext(request *protocolv2.RequestContext) *protocolv2.ResponseContext {
	result := &protocolv2.ResponseContext{ServerTime: timestamppb.New(time.Now().UTC())}
	if request != nil {
		result.RequestId = request.GetRequestId()
		if request.GetTrace() != nil {
			result.Trace = proto.Clone(request.GetTrace()).(*protocolv2.TraceContext)
		}
		if request.GetExtension() != nil {
			result.Extension = proto.Clone(request.GetExtension()).(*protocolv2.ExtensionIdentity)
		}
	}
	return result
}

func protocolV2Document(schemaID, schemaVersion string, values map[string]any) (*protocolv2.TypedDocument, error) {
	encoded, err := structpb.NewStruct(values)
	if err != nil {
		return nil, err
	}
	return &protocolv2.TypedDocument{SchemaId: schemaID, SchemaVersion: schemaVersion, Value: encoded}, nil
}

func protocolV2Failure(reason, message string) *protocolv2.ErrorDetail {
	code := protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	retryable := false
	switch {
	case reason == "host.capability_denied":
		code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
	case strings.Contains(reason, "invalid") || strings.Contains(reason, "unknown") || strings.Contains(reason, "forbidden"):
		code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	case strings.Contains(reason, "not_found"):
		code = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND
	case strings.Contains(reason, "timeout"):
		code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	case strings.Contains(reason, "cancelled"):
		code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case strings.Contains(reason, "stale") || strings.Contains(reason, "draining"):
		code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	case strings.Contains(reason, "unavailable"):
		code = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE
		retryable = true
	}
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Retryable: retryable}
}

func protocolV2Unsupported(reason, message string) *protocolv2.ErrorDetail {
	return &protocolv2.ErrorDetail{
		Code: protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, Reason: reason, Message: message,
	}
}

func protocolV2DocumentValues(document *protocolv2.TypedDocument) map[string]any {
	if document == nil || document.GetValue() == nil {
		return map[string]any{}
	}
	return document.GetValue().AsMap()
}
