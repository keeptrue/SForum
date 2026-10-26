package hostapi

import (
	"context"
	"errors"
	"strings"

	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
)

type ProtocolV2ProviderCaller struct {
	ExtensionID       string
	ExtensionVersion  string
	ArtifactDigest    string
	RuntimeInstanceID string
}

type ProtocolV2ProviderInvocation struct {
	Caller          ProtocolV2ProviderCaller
	SlotID          string
	ContractVersion string
	Operation       string
	InputSchema     string
	Input           map[string]any
}

type ProtocolV2ProviderResult struct {
	ProviderID        string
	ProviderExtension string
	RuntimeInstanceID string
	ResponseSchema    string
	Output            map[string]any
	Attempts          int
}

type ProtocolV2ProviderBroker interface {
	InvokeProtocolV2Provider(context.Context, ProtocolV2ProviderInvocation) (ProtocolV2ProviderResult, error)
}

type ProtocolV2ProviderError struct {
	Reason   string
	Err      error
	Attempts int
}

func (e *ProtocolV2ProviderError) Error() string {
	if e == nil || e.Err == nil {
		return "provider broker failed"
	}
	return e.Err.Error()
}

func (e *ProtocolV2ProviderError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (s *protocolV2ServiceDiscoveryServer) InvokeProvider(
	ctx context.Context,
	request *hostv2.ProviderInvokeRequest,
) (*hostv2.ProviderInvokeResponse, error) {
	response := &hostv2.ProviderInvokeResponse{Context: protocolV2ResponseContext(request.GetContext())}
	if s == nil || s.core == nil || s.core.providers == nil {
		response.Error = protocolV2ProviderPublicError("host.provider_broker_unavailable")
		return response, nil
	}
	identity := protocolV2RuntimeIdentityFromContext(ctx)
	if identity == nil {
		response.Error = protocolV2ProviderPublicError("host.provider_caller_unattested")
		return response, nil
	}
	inputSchema := protocolV2TypedSchemaRef(request.GetInput())
	if strings.TrimSpace(request.GetSlotId()) == "" || strings.TrimSpace(request.GetContractVersion()) == "" ||
		strings.TrimSpace(request.GetOperation()) == "" || inputSchema == "" {
		response.Error = protocolV2ProviderPublicError("host.provider_request_invalid")
		return response, nil
	}
	result, err := s.core.providers.InvokeProtocolV2Provider(ctx, ProtocolV2ProviderInvocation{
		Caller: ProtocolV2ProviderCaller{
			ExtensionID: identity.GetExtensionId(), ExtensionVersion: identity.GetExtensionVersion(),
			ArtifactDigest: identity.GetArtifactDigest(), RuntimeInstanceID: identity.GetInstanceId(),
		},
		SlotID: strings.TrimSpace(request.GetSlotId()), ContractVersion: strings.TrimSpace(request.GetContractVersion()),
		Operation: strings.TrimSpace(request.GetOperation()), InputSchema: inputSchema,
		Input: protocolV2DocumentValues(request.GetInput()),
	})
	if err != nil {
		var brokerError *ProtocolV2ProviderError
		if errors.As(err, &brokerError) && brokerError.Attempts > 0 {
			response.Attempts = uint32(brokerError.Attempts)
		}
		response.Error = protocolV2ProviderFailure(err)
		return response, nil
	}
	schemaID, schemaVersion, ok := splitServiceSchemaRef(result.ResponseSchema)
	if !ok {
		response.Error = protocolV2ProviderPublicError("host.provider_response_invalid")
		return response, nil
	}
	output, err := protocolV2Document(schemaID, schemaVersion, result.Output)
	if err != nil {
		response.Error = protocolV2ProviderPublicError("host.provider_response_invalid")
		return response, nil
	}
	response.Output = output
	response.ProviderId = result.ProviderID
	response.ProviderExtensionId = result.ProviderExtension
	response.RuntimeInstanceId = result.RuntimeInstanceID
	response.Attempts = uint32(result.Attempts)
	return response, nil
}

func protocolV2TypedSchemaRef(document *protocolv2.TypedDocument) string {
	if document == nil || document.GetValue() == nil || strings.TrimSpace(document.GetSchemaId()) == "" ||
		strings.TrimSpace(document.GetSchemaVersion()) == "" {
		return ""
	}
	return strings.TrimSpace(document.GetSchemaId()) + "@" + strings.TrimSpace(document.GetSchemaVersion())
}

func protocolV2ProviderFailure(err error) *protocolv2.ErrorDetail {
	var brokerError *ProtocolV2ProviderError
	if errors.As(err, &brokerError) && strings.TrimSpace(brokerError.Reason) != "" {
		return protocolV2ProviderPublicError(brokerError.Reason)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return protocolV2ProviderPublicError("host.provider_timeout")
	case errors.Is(err, context.Canceled):
		return protocolV2ProviderPublicError("host.provider_cancelled")
	default:
		return protocolV2ProviderPublicError("host.provider_invoke_failed")
	}
}

func protocolV2ProviderPublicError(reason string) *protocolv2.ErrorDetail {
	detail := &protocolv2.ErrorDetail{Reason: reason, Message: protocolV2ProviderPublicMessage(reason)}
	switch reason {
	case "host.provider_caller_denied", "host.provider_caller_unattested":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
	case "host.provider_request_invalid":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
	case "host.provider_response_invalid":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	case "host.provider_not_found":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND
		detail.Retryable = true
	case "host.provider_timeout":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED
	case "host.provider_cancelled":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_CANCELLED
	case "host.provider_broker_unavailable":
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE
		detail.Retryable = true
	default:
		detail.Code = protocolv2.ErrorCode_ERROR_CODE_INTERNAL
	}
	return detail
}

func protocolV2ProviderPublicMessage(reason string) string {
	switch reason {
	case "host.provider_request_invalid":
		return "Provider input does not satisfy the declared contract."
	case "host.provider_response_invalid":
		return "Provider output does not satisfy the declared contract."
	case "host.provider_caller_denied":
		return "The calling extension is not authorized to use this provider slot."
	case "host.provider_caller_unattested":
		return "Broker-attested runtime identity is required."
	case "host.provider_not_found":
		return "No compatible provider is currently available."
	case "host.provider_timeout":
		return "The provider call exceeded its declared deadline."
	case "host.provider_cancelled":
		return "The provider call was cancelled."
	default:
		return "The provider call failed."
	}
}
