package hostapi

import (
	"context"
	"strconv"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type protocolV2PermissionServer struct {
	hostv2.UnimplementedPermissionServiceServer
	core *protocolV2Core
}

func (s *protocolV2PermissionServer) Check(ctx context.Context, request *hostv2.PermissionCheckRequest) (*hostv2.PermissionCheckResponse, error) {
	response := &hostv2.PermissionCheckResponse{Context: protocolV2ResponseContext(request.GetContext()), PolicyId: PermissionPolicyID}
	if request.GetResourceType() != "" || request.GetResourceId() != "" {
		response.Error = protocolV2Unsupported("host.permission_resource_unsupported", "The compatibility permission checker does not support resource-scoped policy.")
		return response, nil
	}
	result := s.core.call(ctx, request.GetContext(), MethodCheckPermission, map[string]any{
		"userId": request.GetUserId(), "permission": request.GetPermissionKey(),
	})
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
		return response, nil
	}
	response.Allowed, _ = result.Data["allowed"].(bool)
	if response.Allowed {
		response.Reason = "allowed"
	} else {
		response.Reason = "denied"
	}
	return response, nil
}

func (s *protocolV2PermissionServer) List(_ context.Context, request *hostv2.PermissionListRequest) (*hostv2.PermissionListResponse, error) {
	return &hostv2.PermissionListResponse{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   protocolV2Unsupported("host.permission_catalog_unavailable", "Permission catalog discovery is not available through the legacy Host source."),
	}, nil
}

type protocolV2IdentityServer struct {
	hostv2.UnimplementedIdentityServiceServer
	core *protocolV2Core
}

func (s *protocolV2IdentityServer) GetUser(ctx context.Context, request *hostv2.IdentityUserRequest) (*hostv2.IdentityUserResponse, error) {
	response := &hostv2.IdentityUserResponse{Context: protocolV2ResponseContext(request.GetContext())}
	// declared_fields is a projection request, not authority. The Host resolves
	// every extension field against the live Registry declaration and a live
	// actor permission check inside GetUserSafe.
	payload := map[string]any{
		"userId":         request.GetUserId(),
		"actorUserId":    request.GetContext().GetActor().GetUserId(),
		"declaredFields": append([]string(nil), request.GetDeclaredFields()...),
	}
	result := s.core.call(ctx, request.GetContext(), MethodGetUserSafe, payload)
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
		return response, nil
	}
	user, _ := result.Data["user"].(map[string]any)
	document, err := protocolV2Document(IdentitySafeUserSchemaID, IdentitySafeUserSchemaV1, user)
	if err != nil {
		response.Error = protocolV2Failure("host.identity_encode_failed", err.Error())
		return response, nil
	}
	response.User = document
	return response, nil
}

func (s *protocolV2IdentityServer) InvokeProvider(_ context.Context, request *hostv2.IdentityProviderRequest) (*hostv2.IdentityProviderResponse, error) {
	return &hostv2.IdentityProviderResponse{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   protocolV2Unsupported("host.identity_provider_unavailable", "Identity provider invocation has no legacy Host adapter."),
	}, nil
}

type protocolV2AuditServer struct {
	hostv2.UnimplementedAuditServiceServer
	core *protocolV2Core
}

func (s *protocolV2AuditServer) Append(ctx context.Context, request *hostv2.AuditAppendRequest) (*hostv2.AuditAppendResponse, error) {
	response := &hostv2.AuditAppendResponse{Context: protocolV2ResponseContext(request.GetContext())}
	if request.GetMetadata() != nil && (request.GetMetadata().GetSchemaId() == "" || request.GetMetadata().GetSchemaVersion() == "") {
		response.Error = &protocolv2.ErrorDetail{
			Code: protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "host.audit_metadata_contract_required", Message: "Audit metadata schema id and version are required.",
		}
		return response, nil
	}
	metadata := protocolV2DocumentValues(request.GetMetadata())
	metadata["targetType"] = request.GetTargetType()
	metadata["targetId"] = request.GetTargetId()
	payload := map[string]any{
		"action": request.GetAction(), "metadata": metadata,
		"actorUserId": request.GetContext().GetActor().GetUserId(),
	}
	if request.GetTargetType() == "user" && request.GetTargetId() != "" {
		targetID, err := strconv.ParseInt(request.GetTargetId(), 10, 64)
		if err != nil || targetID <= 0 {
			response.Error = &protocolv2.ErrorDetail{
				Code: protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, Reason: "host.audit_target_invalid", Message: "User target id must be a positive integer.",
			}
			return response, nil
		}
		payload["targetUserId"] = targetID
	}
	result := s.core.call(ctx, request.GetContext(), MethodAppendAudit, payload)
	if !result.OK {
		response.Error = protocolV2Failure(result.Reason, result.Message)
	}
	return response, nil
}

func (s *protocolV2AuditServer) List(_ context.Context, request *hostv2.AuditListRequest) (*hostv2.AuditListResponse, error) {
	return &hostv2.AuditListResponse{
		Context: protocolV2ResponseContext(request.GetContext()),
		Error:   protocolV2Unsupported("host.audit_list_unavailable", "Audit reads have no legacy Host adapter."),
	}, nil
}
