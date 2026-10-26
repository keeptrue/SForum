package hostapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// ProtocolV2CacheServiceServer exposes declaration-bound cache operations to an
// exact broker-attested runtime. Cross-RPC lock tokens are opaque Host handles;
// backend owner tokens and executable callbacks never cross the broker.
type ProtocolV2CacheServiceServer struct {
	hostv2.UnimplementedCacheServiceServer
	service *HostCacheService
	leases  *protocolV2CacheLeaseRegistry
}

func NewProtocolV2CacheServiceServer(service *HostCacheService) (*ProtocolV2CacheServiceServer, error) {
	if service == nil {
		return nil, ErrHostCacheInvalid
	}
	return &ProtocolV2CacheServiceServer{service: service, leases: newProtocolV2CacheLeaseRegistry()}, nil
}

func (s *ProtocolV2CacheServiceServer) Get(
	ctx context.Context,
	request *hostv2.CacheGetRequest,
) (*hostv2.CacheGetResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheGetResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	result, err := s.service.Get(ctx, HostCacheGetRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Key:                  request.GetKey(),
		Schema:               HostCacheSchema{ID: request.GetValueSchemaId(), Version: request.GetValueSchemaVersion()},
	})
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	if !result.Found {
		return response, nil
	}
	var document map[string]any
	if len(result.Value) == 0 || json.Unmarshal(result.Value, &document) != nil || document == nil {
		response.Error = protocolV2CacheFailure(ErrHostCachePoisoned)
		return response, nil
	}
	value, err := structpb.NewStruct(document)
	if err != nil {
		response.Error = protocolV2CacheFailure(ErrHostCachePoisoned)
		return response, nil
	}
	response.Found = true
	response.Revision = result.Revision
	response.Value = &protocolv2.TypedDocument{
		SchemaId: request.GetValueSchemaId(), SchemaVersion: request.GetValueSchemaVersion(), Value: value,
	}
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) Set(
	ctx context.Context,
	request *hostv2.CacheSetRequest,
) (*hostv2.CacheSetResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheSetResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if request.GetValue() == nil || request.GetValue().GetValue() == nil || request.GetTtl() == nil ||
		request.GetTtl().CheckValid() != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	value, err := json.Marshal(request.GetValue().GetValue().AsMap())
	if err != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	revision, err := s.service.Set(ctx, HostCacheSetRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Key:                  request.GetKey(),
		Schema:               HostCacheSchema{ID: request.GetValue().GetSchemaId(), Version: request.GetValue().GetSchemaVersion()},
		Value:                value, TTL: request.GetTtl().AsDuration(), Tags: request.GetTags(),
		ExpectedRevision: request.GetExpectedRevision(),
	})
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Revision = revision
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) Delete(
	ctx context.Context,
	request *hostv2.CacheDeleteRequest,
) (*hostv2.CacheDeleteResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheDeleteResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	deleted, err := s.service.Delete(ctx, HostCacheDeleteRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Key:                  request.GetKey(),
	})
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Deleted = deleted
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) Increment(
	ctx context.Context,
	request *hostv2.CacheIncrementRequest,
) (*hostv2.CacheIncrementResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheIncrementResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if request.GetTtl() == nil || request.GetTtl().CheckValid() != nil {
		response.Error = protocolV2CacheFailure(ErrHostCacheInvalid)
		return response, nil
	}
	value, err := s.service.Increment(ctx, HostCacheIncrementRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Key:                  request.GetKey(),
		Delta:                request.GetDelta(),
		TTL:                  request.GetTtl().AsDuration(),
	})
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.Value = value
	return response, nil
}

func (s *ProtocolV2CacheServiceServer) InvalidateTags(
	ctx context.Context,
	request *hostv2.CacheInvalidateRequest,
) (*hostv2.CacheInvalidateResponse, error) {
	identity, caller, detail := protocolV2CacheCaller(ctx, request.GetContext())
	response := &hostv2.CacheInvalidateResponse{Context: protocolV2CacheResponseContext(request.GetContext(), identity)}
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	invalidated, err := s.service.InvalidateTags(ctx, HostCacheInvalidateTagsRequest{
		HostCacheRequestBase: protocolV2CacheRequestBase(caller, request.GetContext(), request.GetNamespace()),
		Tags:                 request.GetTags(),
	})
	if err != nil {
		response.Error = protocolV2CacheFailure(err)
		return response, nil
	}
	response.InvalidatedEntries = invalidated
	return response, nil
}

func protocolV2CacheCaller(
	ctx context.Context,
	request *protocolv2.RequestContext,
) (*protocolv2.ExtensionIdentity, HostCacheCaller, *protocolv2.ErrorDetail) {
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if identity == nil || strings.TrimSpace(identity.GetExtensionId()) == "" ||
		strings.TrimSpace(identity.GetExtensionVersion()) == "" || !validHostCacheDigest(identity.GetArtifactDigest()) ||
		strings.TrimSpace(identity.GetInstanceId()) == "" {
		return identity, HostCacheCaller{}, protocolV2CacheFailure(ErrHostCacheStale)
	}
	if request == nil || !proto.Equal(request.GetExtension(), identity) {
		return identity, HostCacheCaller{}, protocolV2CacheFailure(ErrHostCacheStale)
	}
	if request.GetActor() != nil {
		return identity, HostCacheCaller{}, protocolV2CacheFailure(ErrHostCacheScopeRequired)
	}
	return identity, HostCacheCaller{
		ExtensionID: identity.GetExtensionId(), ExtensionVersion: identity.GetExtensionVersion(),
		ArtifactDigest: identity.GetArtifactDigest(), RuntimeInstanceID: identity.GetInstanceId(), Attested: true,
		versionFromHostPlan: true,
	}, nil
}

func protocolV2CacheRequestBase(
	caller HostCacheCaller,
	request *protocolv2.RequestContext,
	namespace string,
) HostCacheRequestBase {
	locale := ""
	if request != nil {
		locale = request.GetLocale()
	}
	return HostCacheRequestBase{Caller: caller, Namespace: namespace, Scope: HostCacheScope{Locale: locale}}
}

func protocolV2CacheResponseContext(
	request *protocolv2.RequestContext,
	identity *protocolv2.ExtensionIdentity,
) *protocolv2.ResponseContext {
	response := &protocolv2.ResponseContext{ServerTime: timestamppb.Now()}
	if request != nil {
		response.RequestId = request.GetRequestId()
		if request.GetTrace() != nil {
			response.Trace = proto.Clone(request.GetTrace()).(*protocolv2.TraceContext)
		}
	}
	if identity != nil {
		response.Extension = proto.Clone(identity).(*protocolv2.ExtensionIdentity)
	}
	return response
}

func protocolV2CacheFailure(err error) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: "host.cache_failed", Message: "The Host cache operation failed."}
	switch {
	case errors.Is(err, context.Canceled):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
		detail.Reason = "host.cache_cancelled"
		detail.Message = "The Host cache operation was cancelled."
	case errors.Is(err, context.DeadlineExceeded):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
		detail.Reason = "host.cache_deadline"
		detail.Message = "The Host cache operation exceeded its deadline."
	case errors.Is(err, ErrHostCacheInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		detail.Reason = "host.cache_request_invalid"
		detail.Message = "The cache request does not satisfy the declared contract."
	case errors.Is(err, ErrHostCacheDenied):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.cache_caller_denied"
		detail.Message = "The exact runtime does not own this cache namespace."
	case errors.Is(err, ErrHostCacheScopeRequired):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
		detail.Reason = "host.cache_scope_unattested"
		detail.Message = "This cache policy requires Host-attested actor or permission scope."
	case errors.Is(err, ErrHostCacheStale):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_STALE_RUNTIME
		detail.Reason = "host.cache_runtime_stale"
		detail.Message = "The cache declaration or exact runtime is stale."
	case errors.Is(err, ErrHostCacheConflict):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CONFLICT
		detail.Reason = "host.cache_revision_conflict"
		detail.Message = "The cached value changed before this write."
	case errors.Is(err, ErrHostCachePoisoned):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.cache_value_invalid"
		detail.Message = "The cached value does not satisfy the requested schema."
	case errors.Is(err, ErrHostCacheLockNotOwned):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		detail.Reason = "host.cache_lock_not_owned"
		detail.Message = "The cache lock expired, was consumed, or belongs to another caller."
	case errors.Is(err, ErrHostCacheProviderUnavailable), errors.Is(err, ErrHostCacheProviderInvalid):
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE
		detail.Reason = "host.cache_provider_unavailable"
		detail.Message = "No admitted cache provider can complete this operation."
		detail.Retryable = true
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	}
	return detail
}

var _ hostv2.CacheServiceServer = (*ProtocolV2CacheServiceServer)(nil)
