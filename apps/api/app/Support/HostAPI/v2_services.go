package hostapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"

	"github.com/keeptrue/sforum/apps/api/app/Support/Capabilities"
	hostv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/host/v2"
	protocolv2 "github.com/keeptrue/sforum/apps/api/sdk/plugin/v2/gen/sforum/protocol/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	defaultServiceListLimit = 100
	maxServiceListLimit     = 200
)

type protocolV2ServiceDiscoveryServer struct {
	hostv2.UnimplementedServiceDiscoveryServiceServer
	core *protocolV2Core
}

func (s *protocolV2ServiceDiscoveryServer) List(ctx context.Context, request *hostv2.ServiceListRequest) (*hostv2.ServiceListResponse, error) {
	response := &hostv2.ServiceListResponse{Context: protocolV2ResponseContext(request.GetContext()), Page: &protocolv2.PageInfo{}}
	registry := s.registry()
	if registry == nil {
		response.Error = serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.service_registry_unavailable", "Service discovery is not configured.", true)
		return response, nil
	}
	serviceID := strings.TrimSpace(request.GetServiceId())
	constraint := strings.TrimSpace(request.GetVersionConstraint())
	services, err := registry.List(serviceID, constraint)
	if err != nil {
		response.Error = serviceRegistryError(err)
		return response, nil
	}
	listRevision := registry.Revision()
	if len(services) > 0 {
		listRevision = services[0].Revision
	}

	caller := serviceCallerFromContexts(ctx, request.GetContext())
	if detail := s.requireExtensionsCall(ctx, caller); detail != nil {
		response.Error = detail
		return response, nil
	}
	authorized := services[:0]
	for _, service := range services {
		if service.Authorize(caller).Allowed && service.AuthorizeDependency(caller).Allowed {
			authorized = append(authorized, service)
		}
	}
	start, limit, cursorError := decodeServicePage(request.GetPage(), listRevision, serviceID, constraint, len(authorized))
	if cursorError != nil {
		response.Error = cursorError
		return response, nil
	}
	end := start + limit
	if end > len(authorized) {
		end = len(authorized)
	}
	response.Services = make([]*protocolv2.ServiceDescriptor, 0, end-start)
	for _, service := range authorized[start:end] {
		response.Services = append(response.Services, resolvedServiceDescriptor(service))
	}
	if end < len(authorized) {
		cursor, err := encodeServicePageCursor(servicePageCursor{
			Revision: listRevision, Offset: end, ServiceID: serviceID, Constraint: constraint,
		})
		if err != nil {
			response.Error = serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.service_cursor_encode_failed", "Service discovery cursor creation failed.", false)
			return response, nil
		}
		response.Page.NextCursor = cursor
		response.Page.HasMore = true
	}
	return response, nil
}

func (s *protocolV2ServiceDiscoveryServer) Resolve(ctx context.Context, request *hostv2.ServiceResolveRequest) (*hostv2.ServiceResolveResponse, error) {
	response := &hostv2.ServiceResolveResponse{Context: protocolV2ResponseContext(request.GetContext())}
	resolved, detail := s.resolve(ctx, request.GetContext(), request.GetServiceId(), request.GetVersionConstraint(), false)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	response.Service = resolvedServiceDescriptor(resolved)
	response.ProviderExtensionId = resolved.Winner.ExtensionID
	response.RegistryRevision = resolved.Revision
	return response, nil
}

func (s *protocolV2ServiceDiscoveryServer) Invoke(ctx context.Context, request *hostv2.ServiceInvokeRequest) (*hostv2.ServiceInvokeResponse, error) {
	response := &hostv2.ServiceInvokeResponse{Context: protocolV2ResponseContext(request.GetContext())}
	operation := strings.TrimSpace(request.GetOperation())
	if operation == "" {
		response.Error = serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_operation_required", "Service operation is required.", false)
		return response, nil
	}
	version := strings.TrimSpace(request.GetVersion())
	resolved, detail := s.resolve(ctx, request.GetContext(), request.GetServiceId(), version, true)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	if resolved.Winner.Descriptor.GetClientStreaming() || resolved.Winner.Descriptor.GetServerStreaming() {
		response.Error = serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_stream_required", "The resolved service must be invoked through the streaming API.", false)
		return response, nil
	}
	if detail := validateServiceDocument(request.GetInput(), resolved.Winner.Descriptor.GetRequestSchemaId(), "input"); detail != nil {
		response.Error = detail
		return response, nil
	}
	lease, detail := s.acquireProvider(ctx, resolved.Winner)
	if detail != nil {
		response.Error = detail
		return response, nil
	}
	defer lease.Release()

	providerContext := cloneServiceRequestContext(request.GetContext())
	output, remoteError, err := resolved.Winner.Provider.Invoke(
		lease.Context(), providerContext, resolved.Winner.Descriptor.GetServiceId(),
		resolved.Winner.Descriptor.GetVersion(), operation, cloneServiceDocument(request.GetInput()),
	)
	if err != nil {
		response.Error = s.providerCallFailure(lease, err)
		return response, nil
	}
	if lease.Context().Err() != nil {
		response.Error = s.providerCallFailure(lease, context.Cause(lease.Context()))
		return response, nil
	}
	if remoteError != nil {
		response.Error = cloneServiceError(remoteError)
		return response, nil
	}
	if detail := validateServiceDocument(output, resolved.Winner.Descriptor.GetResponseSchemaId(), "output"); detail != nil {
		response.Error = detail
		return response, nil
	}
	response.Output = cloneServiceDocument(output)
	return response, nil
}

func (s *protocolV2ServiceDiscoveryServer) Stream(stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame]) error {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return status.Error(codes.InvalidArgument, "service stream open frame is required")
		}
		return err
	}
	open := first.GetOpen()
	if open == nil {
		return sendServiceStreamError(stream, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_stream_open_required", "The first service stream frame must contain open.", false))
	}
	operation := strings.TrimSpace(open.GetOperation())
	if operation == "" {
		return sendServiceStreamError(stream, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_operation_required", "Service operation is required.", false))
	}
	resolved, detail := s.resolve(stream.Context(), open.GetContext(), open.GetServiceId(), open.GetVersion(), true)
	if detail != nil {
		return sendServiceStreamError(stream, detail)
	}
	if !resolved.Winner.Descriptor.GetClientStreaming() && !resolved.Winner.Descriptor.GetServerStreaming() {
		return sendServiceStreamError(stream, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_streaming_unsupported", "The resolved service does not declare streaming.", false))
	}
	provider, ok := resolved.Winner.Provider.(ServiceStreamingProvider)
	if !ok {
		return sendServiceStreamError(stream, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.service_stream_provider_unavailable", "The resolved service provider cannot accept streams.", true))
	}
	lease, detail := s.acquireProvider(stream.Context(), resolved.Winner)
	if detail != nil {
		return returnServiceStreamFailure(stream, detail)
	}
	defer lease.Release()
	var unaryInput *protocolv2.TypedDocument
	if !resolved.Winner.Descriptor.GetClientStreaming() {
		var inputError *protocolv2.ErrorDetail
		unaryInput, inputError, err = receiveUnaryServiceInput(lease.Context(), stream, resolved.Winner.Descriptor.GetRequestSchemaId())
		if err != nil {
			if lease.Context().Err() != nil {
				return s.sendProviderStreamFailure(stream, lease, err)
			}
			return err
		}
		if inputError != nil {
			return sendServiceStreamError(stream, inputError)
		}
	}

	adapter := &protocolV2ServiceBidiStream{
		ctx: lease.Context(), stream: stream, requestSchemaID: resolved.Winner.Descriptor.GetRequestSchemaId(),
		responseSchemaID: resolved.Winner.Descriptor.GetResponseSchemaId(),
		clientStreaming:  resolved.Winner.Descriptor.GetClientStreaming(),
		serverStreaming:  resolved.Winner.Descriptor.GetServerStreaming(),
		unaryInput:       unaryInput,
	}
	remoteError, err := provider.Stream(
		lease.Context(), cloneServiceRequestContext(open.GetContext()),
		resolved.Winner.Descriptor.GetServiceId(), resolved.Winner.Descriptor.GetVersion(), operation, adapter,
	)
	if err != nil {
		var validation *serviceStreamValidationError
		switch {
		case errors.As(err, &validation):
			return sendServiceStreamError(stream, validation.detail)
		case lease.Context().Err() != nil:
			return s.sendProviderStreamFailure(stream, lease, err)
		case errors.Is(err, context.Canceled):
			return status.Error(codes.Canceled, "service stream cancelled")
		case errors.Is(err, context.DeadlineExceeded):
			return status.Error(codes.DeadlineExceeded, "service stream deadline exceeded")
		default:
			return sendServiceStreamError(stream, serviceProviderFailure(err))
		}
	}
	if lease.Context().Err() != nil {
		return s.sendProviderStreamFailure(stream, lease, context.Cause(lease.Context()))
	}
	if remoteError != nil {
		return sendServiceStreamError(stream, cloneServiceError(remoteError))
	}
	if validation := adapter.completionError(); validation != nil {
		return sendServiceStreamError(stream, validation.detail)
	}
	return nil
}

func (s *protocolV2ServiceDiscoveryServer) acquireProvider(ctx context.Context, winner ServiceRegistration) (ServiceProviderAdmissionLease, *protocolv2.ErrorDetail) {
	if s == nil || s.core == nil || s.core.service == nil || s.core.service.serviceAdmission == nil {
		return nil, serviceProviderAdmissionError(ErrServiceProviderAdmissionUnavailable)
	}
	identity := ServiceProviderIdentity{ExtensionID: winner.ExtensionID, InstanceID: winner.InstanceID}
	if !identity.valid() {
		return nil, serviceProviderAdmissionError(ErrServiceProviderStale)
	}
	lease, err := s.core.service.serviceAdmission.AcquireServiceProvider(ctx, identity)
	if err != nil {
		return nil, serviceProviderAdmissionError(err)
	}
	if lease == nil || lease.Context() == nil {
		if lease != nil {
			lease.Release()
		}
		return nil, serviceProviderAdmissionError(ErrServiceProviderAdmissionUnavailable)
	}
	if lease.Context().Err() != nil {
		detail := s.providerCallFailure(lease, context.Cause(lease.Context()))
		lease.Release()
		return nil, detail
	}
	return lease, nil
}

func (s *protocolV2ServiceDiscoveryServer) providerCallFailure(lease ServiceProviderAdmissionLease, fallback error) *protocolv2.ErrorDetail {
	if failure, ok := lease.(ServiceProviderAdmissionLeaseFailure); ok {
		if err := failure.ServiceProviderAdmissionFailure(); err != nil {
			return serviceProviderAdmissionError(err)
		}
	}
	return serviceProviderAdmissionError(fallback)
}

func (s *protocolV2ServiceDiscoveryServer) sendProviderStreamFailure(
	stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame],
	lease ServiceProviderAdmissionLease,
	fallback error,
) error {
	detail := s.providerCallFailure(lease, fallback)
	return returnServiceStreamFailure(stream, detail)
}

func returnServiceStreamFailure(
	stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame],
	detail *protocolv2.ErrorDetail,
) error {
	switch detail.GetCode() {
	case protocolv2.ErrorCode_ERROR_CODE_CANCELLED:
		return status.Error(codes.Canceled, detail.GetMessage())
	case protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED:
		return status.Error(codes.DeadlineExceeded, detail.GetMessage())
	default:
		return sendServiceStreamError(stream, detail)
	}
}

func (s *protocolV2ServiceDiscoveryServer) registry() *ServiceRegistry {
	if s == nil || s.core == nil {
		return nil
	}
	return s.core.services
}

func (s *protocolV2ServiceDiscoveryServer) resolve(ctx context.Context, requestContext *protocolv2.RequestContext, serviceID, version string, exact bool) (ResolvedService, *protocolv2.ErrorDetail) {
	registry := s.registry()
	if registry == nil {
		return ResolvedService{}, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.service_registry_unavailable", "Service discovery is not configured.", true)
	}
	serviceID = strings.TrimSpace(serviceID)
	version = strings.TrimSpace(version)
	if serviceID == "" {
		return ResolvedService{}, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_id_required", "Service id is required.", false)
	}
	if exact && version == "" {
		return ResolvedService{}, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_version_required", "Exact service version is required.", false)
	}
	var (
		resolved ResolvedService
		err      error
	)
	if exact {
		resolved, err = registry.ResolveExact(serviceID, version)
	} else {
		resolved, err = registry.Resolve(serviceID, version)
	}
	if err != nil {
		return ResolvedService{}, serviceRegistryError(err)
	}
	caller := serviceCallerFromContexts(ctx, requestContext)
	if detail := s.requireExtensionsCall(ctx, caller); detail != nil {
		return ResolvedService{}, detail
	}
	decision := resolved.Authorize(caller)
	if !decision.Allowed {
		return ResolvedService{}, &protocolv2.ErrorDetail{
			Code: protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED, Reason: "host.service_authority_denied",
			Message: "The caller does not hold every authority required by the service.",
			Metadata: map[string]string{
				"requiredAuthority": strings.Join(decision.Required, ","),
				"missingAuthority":  strings.Join(decision.Missing, ","),
			},
		}
	}
	if dependency := resolved.AuthorizeDependency(caller); !dependency.Allowed {
		return ResolvedService{}, serviceDependencyError(dependency)
	}
	return resolved, nil
}

// requireExtensionsCall enforces the Host process capability for trusted
// automation service discovery. Wire GrantedAuthority is disclosure only.
func (s *protocolV2ServiceDiscoveryServer) requireExtensionsCall(
	ctx context.Context,
	caller ServiceCaller,
) *protocolv2.ErrorDetail {
	if s == nil || s.core == nil || s.core.service == nil {
		return serviceDiscoveryError(
			protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE,
			"host.service_capability_unavailable",
			"Service discovery capability admission is unavailable.",
			true,
		)
	}
	extensionID := strings.TrimSpace(caller.ExtensionID)
	if extensionID == "" {
		return serviceDiscoveryError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.extensions_call_denied",
			"extensions.call requires an exact plugin runtime identity.",
			false,
		)
	}
	caps, err := s.core.service.loadCaps(ctx, extensionID)
	if err != nil {
		return serviceDiscoveryError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.extensions_call_denied",
			"The caller does not hold extensions.call.",
			false,
		)
	}
	if err := caps.Require(capabilities.ExtensionsCall); err != nil {
		return serviceDiscoveryError(
			protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED,
			"host.extensions_call_denied",
			"The caller does not hold extensions.call.",
			false,
		)
	}
	return nil
}

type servicePageCursor struct {
	Revision   uint64 `json:"revision"`
	Offset     int    `json:"offset"`
	ServiceID  string `json:"serviceId,omitempty"`
	Constraint string `json:"constraint,omitempty"`
}

func decodeServicePage(page *protocolv2.PageRequest, revision uint64, serviceID, constraint string, total int) (int, int, *protocolv2.ErrorDetail) {
	limit := defaultServiceListLimit
	if page != nil && page.GetLimit() != 0 {
		if page.GetLimit() > maxServiceListLimit {
			return 0, 0, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_page_limit_invalid", "Service discovery page limit exceeds the supported maximum.", false)
		}
		limit = int(page.GetLimit())
	}
	if page == nil || strings.TrimSpace(page.GetCursor()) == "" {
		return 0, limit, nil
	}
	encoded := strings.TrimSpace(page.GetCursor())
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return 0, 0, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_cursor_invalid", "Service discovery cursor is invalid.", false)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var cursor servicePageCursor
	if err := decoder.Decode(&cursor); err != nil {
		return 0, 0, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_cursor_invalid", "Service discovery cursor is invalid.", false)
	}
	if err := ensureServiceCursorEOF(decoder); err != nil || cursor.Offset < 0 || cursor.Offset > total || cursor.ServiceID != serviceID || cursor.Constraint != constraint {
		return 0, 0, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_cursor_invalid", "Service discovery cursor does not match this query.", false)
	}
	if cursor.Revision != revision {
		return 0, 0, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_cursor_stale", "Service registry changed after this cursor was issued.", false)
	}
	return cursor.Offset, limit, nil
}

func ensureServiceCursorEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err == nil {
		return errors.New("service cursor has trailing data")
	} else {
		return err
	}
}

func encodeServicePageCursor(cursor servicePageCursor) (string, error) {
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

type protocolV2ServiceBidiStream struct {
	ctx              context.Context
	stream           grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame]
	requestSchemaID  string
	responseSchemaID string
	clientStreaming  bool
	serverStreaming  bool
	unaryInput       *protocolv2.TypedDocument

	stateMu             sync.Mutex
	unaryInputDelivered bool
	outputCount         int
	validation          *serviceStreamValidationError
}

func (s *protocolV2ServiceBidiStream) Context() context.Context {
	return s.ctx
}

func (s *protocolV2ServiceBidiStream) Send(document *protocolv2.TypedDocument) error {
	if detail := validateServiceDocument(document, s.responseSchemaID, "output"); detail != nil {
		return s.fail(detail)
	}
	s.stateMu.Lock()
	if !s.serverStreaming && s.outputCount >= 1 {
		s.stateMu.Unlock()
		return s.fail(serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_output_message_limit", "The resolved service permits exactly one output message.", false))
	}
	s.outputCount++
	s.stateMu.Unlock()
	return sendServiceFrame(s.ctx, s.stream, &hostv2.ServiceStreamFrame{
		Frame: &hostv2.ServiceStreamFrame_Message{Message: cloneServiceDocument(document)},
	})
}

func (s *protocolV2ServiceBidiStream) Recv() (*protocolv2.TypedDocument, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, context.Cause(s.ctx)
	}
	if !s.clientStreaming {
		s.stateMu.Lock()
		if s.unaryInputDelivered {
			s.stateMu.Unlock()
			return nil, io.EOF
		}
		s.unaryInputDelivered = true
		message := cloneServiceDocument(s.unaryInput)
		s.stateMu.Unlock()
		return message, nil
	}
	frame, err := receiveServiceFrame(s.ctx, s.stream)
	if err != nil {
		return nil, err
	}
	message := frame.GetMessage()
	if message == nil {
		return nil, s.fail(serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_stream_frame_invalid", "Service stream frames after open must contain a typed message.", false))
	}
	if detail := validateServiceDocument(message, s.requestSchemaID, "input"); detail != nil {
		return nil, s.fail(detail)
	}
	return cloneServiceDocument(message), nil
}

// A server stream closes its response side when the handler returns. Caller
// half-close is delivered to Recv as io.EOF, which the provider can propagate
// to its plugin-side client stream before continuing to send responses.
func (s *protocolV2ServiceBidiStream) CloseSend() error {
	return nil
}

func (s *protocolV2ServiceBidiStream) fail(detail *protocolv2.ErrorDetail) *serviceStreamValidationError {
	validation := &serviceStreamValidationError{detail: detail}
	s.stateMu.Lock()
	if s.validation == nil {
		s.validation = validation
	} else {
		validation = s.validation
	}
	s.stateMu.Unlock()
	return validation
}

func (s *protocolV2ServiceBidiStream) completionError() *serviceStreamValidationError {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	if s.validation != nil {
		return s.validation
	}
	if !s.clientStreaming && !s.unaryInputDelivered {
		return &serviceStreamValidationError{detail: serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_input_not_consumed", "The provider did not consume the service input message.", false)}
	}
	if !s.serverStreaming && s.outputCount != 1 {
		return &serviceStreamValidationError{detail: serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_output_message_required", "The resolved service must produce exactly one output message.", false)}
	}
	return nil
}

func receiveUnaryServiceInput(ctx context.Context, stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame], expectedSchemaID string) (*protocolv2.TypedDocument, *protocolv2.ErrorDetail, error) {
	frame, err := receiveServiceFrame(ctx, stream)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_input_message_required", "The resolved service requires exactly one input message.", false), nil
		}
		return nil, nil, err
	}
	message := frame.GetMessage()
	if message == nil {
		return nil, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_stream_frame_invalid", "Service stream frames after open must contain a typed message.", false), nil
	}
	if detail := validateServiceDocument(message, expectedSchemaID, "input"); detail != nil {
		return nil, detail, nil
	}
	if _, err := receiveServiceFrame(ctx, stream); err == nil {
		return nil, serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_input_message_limit", "The resolved service permits exactly one input message.", false), nil
	} else if !errors.Is(err, io.EOF) {
		return nil, nil, err
	}
	return cloneServiceDocument(message), nil, nil
}

type serviceFrameResult struct {
	frame *hostv2.ServiceStreamFrame
	err   error
}

// gRPC 的原始 stream context 只受 caller 控制；runtime force-drain 需要用
// lease context 额外中断 provider 侧的阻塞收发。
func receiveServiceFrame(ctx context.Context, stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame]) (*hostv2.ServiceStreamFrame, error) {
	if err := ctx.Err(); err != nil {
		return nil, context.Cause(ctx)
	}
	result := make(chan serviceFrameResult, 1)
	go func() {
		frame, err := stream.Recv()
		result <- serviceFrameResult{frame: frame, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	case received := <-result:
		return received.frame, received.err
	}
}

func sendServiceFrame(ctx context.Context, stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame], frame *hostv2.ServiceStreamFrame) error {
	if err := ctx.Err(); err != nil {
		return context.Cause(ctx)
	}
	// gRPC 只允许一个并发 Send；直接转发避免 force-drain 后错误帧与
	// 尚未退出的发送 goroutine 发生并发写。Provider 仍可由 Context 中断。
	return stream.Send(frame)
}

type serviceStreamValidationError struct {
	detail *protocolv2.ErrorDetail
}

func (e *serviceStreamValidationError) Error() string {
	if e == nil || e.detail == nil {
		return "invalid service stream"
	}
	return e.detail.GetReason() + ": " + e.detail.GetMessage()
}

func validateServiceDocument(document *protocolv2.TypedDocument, expectedSchemaID, direction string) *protocolv2.ErrorDetail {
	expectedID, expectedVersion, contractOK := splitServiceSchemaRef(expectedSchemaID)
	if !contractOK {
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION,
			"host.service_contract_schema_invalid", "The resolved service declares an invalid schema contract.", false)
	}
	if document == nil || strings.TrimSpace(document.GetSchemaId()) == "" || strings.TrimSpace(document.GetSchemaVersion()) == "" {
		code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if direction == "output" {
			code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		}
		return serviceDiscoveryError(code, "host.service_"+direction+"_schema_required", "Service "+direction+" must declare its schema id and version.", false)
	}
	if document.GetSchemaId() != expectedID {
		code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if direction == "output" {
			code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		}
		return serviceDiscoveryError(code, "host.service_"+direction+"_schema_mismatch", "Service "+direction+" schema does not match the resolved contract.", false)
	}
	if document.GetSchemaVersion() != expectedVersion {
		code := protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT
		if direction == "output" {
			code = protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
		}
		return serviceDiscoveryError(code, "host.service_"+direction+"_schema_version_mismatch", "Service "+direction+" schema version does not match the resolved contract.", false)
	}
	return nil
}

func serviceCallerFromContexts(ctx context.Context, requestContext *protocolv2.RequestContext) ServiceCaller {
	caller := ServiceCaller{}
	if requestContext == nil {
		return caller
	}
	identity := ProtocolV2RuntimeIdentityFromContext(ctx)
	if identity != nil {
		caller.Attested = true
	} else {
		identity = requestContext.GetExtension()
	}
	caller.ExtensionID = identity.GetExtensionId()
	caller.ExtensionVersion = identity.GetExtensionVersion()
	caller.ArtifactDigest = identity.GetArtifactDigest()
	caller.TrustGrantID = identity.GetTrustGrantId()
	caller.RuntimeEpoch = identity.GetRuntimeEpoch()
	caller.InstanceID = identity.GetInstanceId()
	caller.GrantedAuthority = make([]string, 0, len(requestContext.GetGrantedAuthority()))
	for _, grant := range requestContext.GetGrantedAuthority() {
		if grant != nil {
			caller.GrantedAuthority = append(caller.GrantedAuthority, grant.GetKey())
		}
	}
	return caller
}

func serviceDependencyError(decision ServiceDependencyDecision) *protocolv2.ErrorDetail {
	code := protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION
	reason := "host.service_dependency_" + decision.Reason
	message := "The caller's exact runtime dependency does not authorize this service provider."
	if decision.Reason == "undeclared" {
		code = protocolv2.ErrorCode_ERROR_CODE_PERMISSION_DENIED
	}
	metadata := map[string]string{}
	if decision.DependencyTarget != "" {
		metadata["dependencyTarget"] = decision.DependencyTarget
	}
	if decision.VersionConstraint != "" {
		metadata["versionConstraint"] = decision.VersionConstraint
	}
	if decision.ProviderVersion != "" {
		metadata["providerVersion"] = decision.ProviderVersion
	}
	if len(decision.Candidates) > 0 {
		metadata["candidates"] = strings.Join(decision.Candidates, ",")
	}
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Metadata: metadata}
}

func resolvedServiceDescriptor(resolved ResolvedService) *protocolv2.ServiceDescriptor {
	descriptor := proto.Clone(resolved.Winner.Descriptor).(*protocolv2.ServiceDescriptor)
	descriptor.ServiceId = resolved.ServiceID
	return descriptor
}

func serviceRegistryError(err error) *protocolv2.ErrorDetail {
	switch {
	case errors.Is(err, ErrInvalidServiceConstraint), errors.Is(err, ErrInvalidServiceRegistration):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INVALID_ARGUMENT, "host.service_query_invalid", err.Error(), false)
	case errors.Is(err, ErrServiceNotFound):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_NOT_FOUND, "host.service_not_found", "No compatible service provider is registered.", false)
	default:
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_INTERNAL, "host.service_registry_failed", "Service registry lookup failed.", false)
	}
}

func serviceProviderFailure(err error) *protocolv2.ErrorDetail {
	switch {
	case errors.Is(err, context.Canceled):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_CANCELLED, "host.service_cancelled", "Service invocation was cancelled.", false)
	case errors.Is(err, context.DeadlineExceeded):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_DEADLINE_EXCEEDED, "host.service_deadline_exceeded", "Service invocation exceeded its deadline.", false)
	default:
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.service_transport_unavailable", "The service provider transport is unavailable.", true)
	}
}

func serviceProviderAdmissionError(err error) *protocolv2.ErrorDetail {
	switch {
	case errors.Is(err, ErrServiceProviderStale):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_provider_stale", "The resolved service provider is no longer the active runtime instance.", false)
	case errors.Is(err, ErrServiceProviderDraining):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_FAILED_PRECONDITION, "host.service_provider_draining", "The resolved service provider is draining and no longer accepts calls.", true)
	case errors.Is(err, ErrServiceProviderAdmissionUnavailable):
		return serviceDiscoveryError(protocolv2.ErrorCode_ERROR_CODE_UNAVAILABLE, "host.service_provider_admission_unavailable", "Service provider runtime admission is unavailable.", true)
	default:
		return serviceProviderFailure(err)
	}
}

func serviceDiscoveryError(code protocolv2.ErrorCode, reason, message string, retryable bool) *protocolv2.ErrorDetail {
	return &protocolv2.ErrorDetail{Code: code, Reason: reason, Message: message, Retryable: retryable}
}

func cloneServiceRequestContext(value *protocolv2.RequestContext) *protocolv2.RequestContext {
	if value == nil {
		return nil
	}
	cloned := proto.Clone(value).(*protocolv2.RequestContext)
	// Plugin callers cannot attest a forum actor. Preserve tracing and runtime
	// identity, but never propagate caller-supplied session authority.
	cloned.Actor = nil
	return cloned
}

func cloneServiceDocument(value *protocolv2.TypedDocument) *protocolv2.TypedDocument {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.TypedDocument)
}

func cloneServiceError(value *protocolv2.ErrorDetail) *protocolv2.ErrorDetail {
	if value == nil {
		return nil
	}
	return proto.Clone(value).(*protocolv2.ErrorDetail)
}

func sendServiceStreamError(stream grpc.BidiStreamingServer[hostv2.ServiceStreamFrame, hostv2.ServiceStreamFrame], detail *protocolv2.ErrorDetail) error {
	if detail == nil {
		return nil
	}
	return stream.Send(&hostv2.ServiceStreamFrame{Frame: &hostv2.ServiceStreamFrame_Error{Error: cloneServiceError(detail)}})
}

var _ hostv2.ServiceDiscoveryServiceServer = (*protocolV2ServiceDiscoveryServer)(nil)
var _ ServiceBidiStream = (*protocolV2ServiceBidiStream)(nil)
